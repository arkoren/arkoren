package restateschedule

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	restatesdk "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"

	"arkoren.dev/driver/restate"
)

// hostGateway is the [gateway] backed by a [restate.Host].
type hostGateway struct {
	// host provides the native clients and lifecycle shared with the other
	// drivers.
	host *restate.Host
}

// list converts options to ingress send options.
func (sendoptions sendOptions) list() []ingress.SendOption {
	var list []ingress.SendOption

	if sendoptions.idempotencyKey != "" {
		list = append(list, restatesdk.WithIdempotencyKey(sendoptions.idempotencyKey))
	}

	if sendoptions.delay > 0 {
		list = append(list, restatesdk.WithDelay(sendoptions.delay))
	}

	return list
}

// sendObject submits an invocation to a keyed virtual object and returns its
// invocation identifier. Keys are escaped because the SDK interpolates path
// segments directly; the server receives the original logical object key.
// Send failures retain the addressed service and handler as context.
func (hostgateway hostGateway) sendObject(ctx context.Context, service, key, handler string, input any, options sendOptions) (string, error) {
	response, err := ingress.ObjectSend[any](hostgateway.host.Ingress(), service, url.PathEscape(key), handler).Send(ctx, input, options.list()...)

	if err != nil {
		return "", fmt.Errorf("send %s/%s/%s: %w", service, key, handler, err)
	}

	return response.Id(), nil
}

// sendService submits a stateless service invocation and returns its invocation
// identifier. Delay and idempotency settings come from options.
func (hostgateway hostGateway) sendService(ctx context.Context, service, handler string, input any, options sendOptions) (string, error) {
	response, err := ingress.ServiceSend[any](hostgateway.host.Ingress(), service, handler).Send(ctx, input, options.list()...)

	if err != nil {
		return "", fmt.Errorf("send %s/%s: %w", service, handler, err)
	}

	return response.Id(), nil
}

// invalidInvocationID is the admin API's 400 message for an identifier that
// is not in invocation format, which therefore names no invocation.
var invalidInvocationID = []byte("'invocation_id' is invalid")

// cancel requests cancellation of one deferred invocation. Missing, completed,
// and recognized invalid-ID responses count as success; other HTTP failures
// retain their status and bounded response text.
func (hostgateway hostGateway) cancel(ctx context.Context, id string) error {
	response, err := hostgateway.host.Admin(ctx, http.MethodPatch, "/invocations/"+url.PathEscape(id)+"/cancel", nil)

	if err != nil {
		return err
	}

	// Status and error-body reads determine the result; closing the reader is best-effort cleanup.
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNotFound, http.StatusConflict:
		return nil
	case http.StatusBadRequest:
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))

		if err != nil {
			return fmt.Errorf("cancel invocation: status %d: %w", response.StatusCode, err)
		}

		if bytes.Contains(body, invalidInvocationID) {
			return nil
		}

		return fmt.Errorf("cancel invocation: status %d: %s", response.StatusCode, bytes.TrimSpace(body))
	default:
		return fmt.Errorf("cancel invocation: status %d", response.StatusCode)
	}
}
