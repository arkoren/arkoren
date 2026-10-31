package restateworkflow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
)

// translate maps an ingress error to a gateway error.
func translate(err error) error {
	if err == nil {
		return nil
	}

	var notFound *ingress.InvocationNotFoundError

	if errors.As(err, &notFound) {
		return errMissing
	}

	var notReady *ingress.InvocationNotReadyError

	if errors.As(err, &notReady) {
		return errNotReady
	}

	var ingressErr ingress.Error

	if errors.As(err, &ingressErr) && ingressErr.IsInvocationError() {
		return &failure{
			code:    int(ingressErr.Code()),
			message: ingressErr.Message(),
		}
	}

	return err
}

// start submits the requested workflow using the native gateway. Its return
// values preserve acceptance or failure for the driver to translate.
func (hostgateway hostGateway) start(ctx context.Context, service, id string, in []byte, request startRequest) (bool, error) {
	var options []ingress.SendOption

	if request.delay > 0 {
		options = append(options, restatesdk.WithDelay(request.delay))
	}

	if len(request.headers) > 0 {
		options = append(options, restatesdk.WithHeaders(request.headers))
	}

	response, err := ingress.WorkflowSend[payload](hostgateway.host.Ingress(), service, escapeKey(id), runHandler).Send(ctx, payload(in), options...)

	if err != nil {
		return false, translate(err)
	}

	return response.Status() != "PreviouslyAccepted", nil
}

// attach waits for the named workflow's output and translates native missing,
// pending, and terminal failures at the gateway boundary.
func (hostgateway hostGateway) attach(ctx context.Context, service, id string) ([]byte, error) {
	out, err := ingress.WorkflowHandle[payload](hostgateway.host.Ingress(), service, escapeKey(id)).Attach(ctx)

	return out, translate(err)
}

// output reads the workflow result without waiting and translates native
// failures, including the not-ready sentinel.
func (hostgateway hostGateway) output(ctx context.Context, service, id string) ([]byte, error) {
	out, err := ingress.WorkflowHandle[payload](hostgateway.host.Ingress(), service, escapeKey(id)).Output(ctx)

	return out, translate(err)
}

// signal sends the signal request to the addressed workflow and returns the
// translated native failure, if any.
func (hostgateway hostGateway) signal(ctx context.Context, service, id string, request signalRequest) error {
	_, err := ingress.Workflow[signalRequest, restatesdk.Void](hostgateway.host.Ingress(), service, escapeKey(id), signalHandler).Request(ctx, request)

	return translate(err)
}

// interrupt calls the workflow's cancellation handler so an active durable wait
// can be interrupted before administrative cancellation.
func (hostgateway hostGateway) interrupt(ctx context.Context, service, id string) error {
	_, err := ingress.Workflow[restatesdk.Void, restatesdk.Void](hostgateway.host.Ingress(), service, escapeKey(id), cancelHandler).Request(ctx, restatesdk.Void{})

	return translate(err)
}

// lookup queries the most recent matching Run invocation. Missing rows return
// errMissing; malformed rows and query failures remain errors.
func (hostgateway hostGateway) lookup(ctx context.Context, service, id string) (invocationInfo, error) {
	query := "SELECT id, status FROM sys_invocation WHERE target_service_name = " + quote(service) +
		" AND target_service_key = " + quote(id) +
		" AND target_handler_name = " + quote(runHandler) +
		" ORDER BY created_at DESC LIMIT 1"

	rows, err := hostgateway.host.Query(ctx, query)

	if err != nil {
		return invocationInfo{}, err
	}

	if len(rows) == 0 {
		return invocationInfo{}, errMissing
	}

	invocation, _ := rows[0]["id"].(string)
	status, _ := rows[0]["status"].(string)

	if invocation == "" {
		return invocationInfo{}, errors.New("restate query: row without invocation id")
	}

	return invocationInfo{
		id:        invocation,
		completed: status == "completed",
	}, nil
}

// cancel requests administrative cancellation of one physical invocation.
// An absent invocation returns errMissing; unexpected HTTP statuses remain errors.
func (hostgateway hostGateway) cancel(ctx context.Context, invocation string) error {
	response, err := hostgateway.host.Admin(ctx, http.MethodPatch, "/invocations/"+url.PathEscape(invocation)+"/cancel", nil)

	if err != nil {
		return err
	}

	// The status determines cancellation's result; closing the response is best-effort cleanup.
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusConflict:
		return nil
	case http.StatusNotFound:
		return errMissing
	default:
		return fmt.Errorf("cancel invocation: status %d", response.StatusCode)
	}
}

// escapeKey percent-escapes a run id for use as the workflow key segment of an
// ingress URL path. The SDK formats the key into the path without escaping, so
// an id containing "/", "?" or "#" would otherwise address another key or
// handler. The Restate ingress percent-decodes workflow keys, so the run still
// sees the original id. The SQL in [hostGateway.lookup] uses the raw id, which
// is how Restate stores the key.
func escapeKey(id string) string {
	return url.PathEscape(id)
}

// quote returns s as a SQL string literal.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
