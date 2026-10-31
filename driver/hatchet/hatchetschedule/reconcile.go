package hatchetschedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"arkoren.dev/core/service/schedule"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	"github.com/hatchet-dev/hatchet/sdks/go/features"
)

const (
	// metadataPrefix marks cron resources owned by one configured driver prefix;
	// foreign values are not reconciled.
	metadataPrefix = "arkoren_prefix"
	// metadataHash stores the workflow-and-expression digest used to retain
	// unchanged cron resources.
	metadataHash = "arkoren_hash"
	// pageSize bounds each cron-list request; reconciliation continues until a
	// shorter page is returned.
	pageSize = int64(100)
)

// desiredCron is a cron trigger an entry requires.
type desiredCron struct {
	// name identifies the application definition represented by this record.
	name string
	// workflow names the engine workflow that owns this cron trigger.
	workflow string
	// expression is the engine-ready cron expression, including any explicit time
	// zone.
	expression string
}

// cronKey identifies a cron within the driver's workflows. It keeps logical
// workflow names so existing ownership hashes remain stable across this fix.
type cronKey struct {
	// workflow names the engine workflow that owns this cron trigger.
	workflow string

	// name identifies the application definition represented by this record.
	name string
}

// existingCron is a cron found in Hatchet.
type existingCron struct {
	// key identifies the resource within its owning workflow or signal scope.
	key cronKey
	// id identifies the engine resource represented by this record.
	id string
	// hash retains the definition digest used to detect stale cron settings.
	hash string
}

// expression maps spec to a Hatchet cron expression.
func (driver *Driver) expression(entry schedule.Entry) (string, error) {
	spec := entry.Spec()
	expression := spec.Expression()

	location := spec.Location()

	if location == nil || location.String() == "UTC" {
		return expression, nil
	}

	if !driver.config.AllowCronTZ {
		return "", fmt.Errorf("entry %q in %s: hatchet evaluates crons in UTC: %w", entry.Name(), location, schedule.ErrUnsupported)
	}

	return "CRON_TZ=" + location.String() + " " + expression, nil
}

// hashCron returns a short stable digest of workflow and expression.
func hashCron(workflow, expression string) string {
	sum := sha256.Sum256([]byte(workflow + "\x00" + expression))

	return hex.EncodeToString(sum[:8])
}

// desired maps entries to the crons that must exist.
func (driver *Driver) desired(entries []schedule.Entry) ([]desiredCron, error) {
	crons := make([]desiredCron, 0, len(entries))

	for _, entry := range entries {
		expression, err := driver.expression(entry)

		if err != nil {
			return nil, err
		}

		workflow := driver.runWorkflow()

		if !entry.Overlapping() {
			workflow = driver.exclusiveWorkflow()
		}

		crons = append(crons, desiredCron{
			name:       entry.Name(),
			workflow:   workflow,
			expression: expression,
		})
	}

	return crons, nil
}

// reconcile makes the owned crons match entries, completing both inventories
// before deleting stale rows. Any deletion failure prevents creation; missing
// definitions are then created in declaration order rather than map order.
func (driver *Driver) reconcile(ctx context.Context, crons cronAPI, entries []schedule.Entry) error {
	wanted, err := driver.desired(entries)

	if err != nil {
		return err
	}

	existing, err := driver.cronInventory(ctx, crons)

	if err != nil {
		return err
	}

	missing, err := driver.removeStaleCrons(ctx, crons, wanted, existing)

	if err != nil {
		return err
	}

	for _, cron := range wanted {
		if _, absent := missing[cronKey{cron.workflow, cron.name}]; !absent {
			continue
		}

		if err := driver.createCron(ctx, crons, cron); err != nil {
			return err
		}
	}

	return nil
}

// cronInventory reads both recurring task inventories before any mutation. A
// failed second listing must not leave a partially reconciled first workflow.
func (driver *Driver) cronInventory(ctx context.Context, crons cronAPI) ([]existingCron, error) {
	var existing []existingCron

	for _, workflow := range []string{driver.runWorkflow(), driver.exclusiveWorkflow()} {
		found, err := driver.listCrons(ctx, crons, workflow)

		if err != nil {
			return nil, err
		}

		existing = append(existing, found...)
	}

	return existing, nil
}

// removeStaleCrons retains the first matching row per logical key and removes
// stale or duplicate owned rows in inventory order. Its remaining map identifies
// missing definitions; the caller still creates them in declaration order.
func (driver *Driver) removeStaleCrons(ctx context.Context, crons cronAPI, wanted []desiredCron, existing []existingCron) (map[cronKey]desiredCron, error) {
	missing := make(map[cronKey]desiredCron, len(wanted))

	for _, cron := range wanted {
		missing[cronKey{cron.workflow, cron.name}] = cron
	}

	for _, cron := range existing {
		target, declared := missing[cron.key]

		if declared && cron.hash == hashCron(target.workflow, target.expression) {
			delete(missing, cron.key)
			continue
		}

		if err := driver.deleteCron(ctx, crons, cron); err != nil {
			return nil, err
		}
	}

	return missing, nil
}

// deleteCron normalizes the SDK's benign missing/204 responses and records only
// successful removal. Other failures stop reconciliation before replacement.
func (driver *Driver) deleteCron(ctx context.Context, crons cronAPI, cron existingCron) error {
	if err := crons.Delete(ctx, cron.id); err != nil && !isNotFound(err) && !isNoContent(err) {
		return fmt.Errorf("delete cron %q: %w", cron.key.name, err)
	}

	driver.logger.InfoContext(ctx, "deleted hatchet cron", "name", cron.key.name, "workflow", cron.key.workflow)

	return nil
}

// createCron creates cron and treats an existing one as success.
func (driver *Driver) createCron(ctx context.Context, crons cronAPI, cron desiredCron) error {
	_, err := crons.Create(ctx, driver.cronWorkflow(cron.workflow), features.CreateCronTrigger{
		Name:       cron.name,
		Expression: cron.expression,
		Input:      map[string]any{"name": cron.name},
		AdditionalMetadata: map[string]any{
			metadataPrefix: driver.config.Prefix,
			metadataHash:   hashCron(cron.workflow, cron.expression),
		},
	})

	if err != nil && !isConflict(err) {
		return fmt.Errorf("create cron %q: %w", cron.name, err)
	}

	return nil
}

// listCrons pages through the crons of workflow that carry this prefix.
func (driver *Driver) listCrons(ctx context.Context, crons cronAPI, workflow string) ([]existingCron, error) {
	var result []existingCron
	full := driver.cronWorkflow(workflow)

	for offset := int64(0); ; offset += pageSize {
		params := rest.CronWorkflowListParams{
			Offset:       &offset,
			Limit:        new(pageSize),
			WorkflowName: &full,
		}

		page, err := crons.List(ctx, params)

		if err != nil {
			return nil, fmt.Errorf("list crons of %q: %w", workflow, err)
		}

		if page == nil {
			// The pinned SDK drops non-200 statuses and returns a nil page.
			return nil, fmt.Errorf("list crons of %q: missing successful response", workflow)
		}

		if page.Rows == nil {
			return result, nil
		}

		for _, row := range *page.Rows {
			if cron, owned := driver.owned(workflow, row); owned {
				result = append(result, cron)
			}
		}

		if int64(len(*page.Rows)) < pageSize {
			return result, nil
		}
	}
}

// cronWorkflow applies the SDK's namespace rule at the REST boundary. Unlike
// scheduled runs, the native cron client does not add a namespace itself.
// Already-prefixed names stay unchanged, matching native task registration.
func (driver *Driver) cronWorkflow(name string) string {
	namespace := driver.host.Namespace()

	return namespace + strings.TrimPrefix(name, namespace)
}

// owned converts row when it carries this driver's prefix.
func (driver *Driver) owned(workflow string, row rest.CronWorkflows) (existingCron, bool) {
	if row.AdditionalMetadata == nil {
		return existingCron{}, false
	}

	metadata := *row.AdditionalMetadata

	if prefix, _ := metadata[metadataPrefix].(string); prefix != driver.config.Prefix {
		return existingCron{}, false
	}

	hash, _ := metadata[metadataHash].(string)

	name := ""

	if row.Name != nil {
		name = *row.Name
	}

	return existingCron{
		key: cronKey{
			workflow,
			name,
		},
		id:   row.Metadata.Id,
		hash: hash,
	}, true
}
