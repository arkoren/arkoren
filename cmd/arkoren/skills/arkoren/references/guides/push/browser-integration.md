---
title: Browser Integration
description: Connect a browser subscription, an authenticated registration endpoint, and a service worker that displays Web Push notifications.
order: 30
---
# Browser Integration

## Introduction

An accepted send cannot display a notification without browser-side code. The
browser creates a subscription; your authenticated application stores it for the
current user; a service worker receives the encrypted message and asks the
operating system to show it. The Web Push driver handles submission and encryption,
not these registration or display steps.

Use an HTTPS application with a signed-in user, an existing
[session authentication flow](../authentication/session-login.md), and a
[persistent VAPID key](vapid-keys.md). Call `identity.VAPIDPublicKey()` on the
loaded `key.Signing` value and serve that public base64url string as
plain text from `GET /push/public-key`. This page uses application-owned routes
`POST /push/subscriptions` and a service worker at `/service-worker.js`; these are
not routes that Arkoren automatically installs.

## Checking browser support

Feature-detect service workers, `PushManager`, and `Notification` rather than
selecting code by browser name. Use a visible “Enable order notifications” button
and request permission only after the user clicks it. Do not prompt on page load.
The [PushManager subscription documentation](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe)
explains the secure-context and user-gesture requirements.

Chrome and Firefox implement standards-based Web Push. Safari supports it on
macOS, starting with Safari 16.1 on macOS Ventura. On iOS and iPadOS 16.4 or later,
the user must add your web app to the Home Screen and open it there; an ordinary
browser tab is not the equivalent permission context. Serve a web app manifest
with `display: "standalone"` or `"fullscreen"` for that installation flow.
[WebKit's platform guidance](https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/)
also confirms that Web Push does not require Apple Developer Program membership
or native APNs credentials. These are platform requirements, not a claim that
this example passed a real-browser test.

## Displaying the notification

The browser decrypts the driver's message into this flat application payload;
there is no `notification` wrapper:

```json
{
  "title": "Order shipped",
  "body": "Your order is on its way.",
  "data": {
    "order_id": "123",
    "url": "/orders/123"
  }
}
```

The driver omits empty body and data fields. `title` is always present. Your
service worker must match this shape rather than expecting a provider's native
notification envelope.

To display the notification, serve the following JavaScript file at
`/service-worker.js` on your application's origin. Its root location gives it
root scope. Use `event.waitUntil` so the browser keeps the worker alive while
the display or click operation completes:

```js
// /service-worker.js displays visible notifications, not silent background jobs.
self.addEventListener("push", (event) => {
  const payload = event.data ? event.data.json() : { title: "Order update" };
  event.waitUntil(self.registration.showNotification(payload.title, {
    body: payload.body,
    data: payload.data,
  }));
});

// A payload URL is navigation input, never permission to open another origin.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  let target = new URL("/", self.location.origin);
  try {
    const requested = new URL(event.notification.data?.url ?? "/", self.location.origin);
    if (requested.origin === self.location.origin) {
      target = requested;
    }
  } catch {
    // Invalid navigation data falls back to the application's home page.
  }
  event.waitUntil(self.clients.openWindow(target.href));
});
```

The click handler opens only your own origin. The destination page still checks
authentication and authorization before showing order 123; possession of a
notification or its data is not permission to read an order. Avoid sensitive text
that could appear on a locked device's notification screen.

## Creating and uploading a subscription

Add a button with `id="enable-push"` and a status element with `id="push-status"`
to your signed-in page. Serve the following as `/push.js` and load it as a module
with `<script type="module" src="/push.js"></script>`. It registers the service
worker and fetches the public key before enabling the button, but does not request
notification permission until the click:

```js
const button = document.querySelector("#enable-push");
const status = document.querySelector("#push-status");
button.disabled = true;

// Decode the public unpadded base64url point, never a private VAPID key.
function publicKeyBytes(encoded) {
  const padded = encoded.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(padded + "=".repeat((4 - padded.length % 4) % 4));
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

try {
  if (!("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window)) {
    throw new Error("Push notifications are not available in this browser context.");
  }
  await navigator.serviceWorker.register("/service-worker.js");
  const registration = await navigator.serviceWorker.ready;
  const response = await fetch("/push/public-key", { credentials: "same-origin" });
  if (!response.ok) {
    throw new Error("Could not load notification configuration.");
  }
  const applicationServerKey = publicKeyBytes((await response.text()).trim());

  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      const permission = await Notification.requestPermission();
      if (permission !== "granted") {
        status.textContent = "Notifications were not enabled. You can change browser permissions later.";
        return;
      }
      const existing = await registration.pushManager.getSubscription();
      const subscription = existing ?? await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey,
      });
      const saved = await fetch("/push/subscriptions", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(subscription.toJSON()),
      });
      if (!saved.ok) {
        throw new Error("Could not save notification preferences.");
      }
      status.textContent = "Order notifications are enabled for this browser.";
    } catch {
      // Do not print the subscription or a potentially sensitive native error.
      status.textContent = "Could not enable notifications. Try again from this page.";
    } finally {
      button.disabled = false;
    }
  });
  button.disabled = false;
} catch (error) {
  status.textContent = error.message;
}
```

`userVisibleOnly: true` commits the application to visible notifications; this is
not a silent background-task transport. `subscription.toJSON()` supplies
`endpoint` and `keys` (`p256dh` and `auth`). A browser can also include
`expirationTime`; it is not needed to send and is not part of `push.Subscription`.
This flow assumes the existing subscription uses the same persisted VAPID public
key. Follow the [key rotation procedure](vapid-keys.md#rotating-the-identity)
instead of reusing a subscription after changing that key.

## Protecting registration

Treat the endpoint as a sensitive capability URL. Do not accept a user ID from
the request body or let one account overwrite another account's registration.
Before mounting this route, configure [CSRF protection](../browser-security/csrf-protection.md),
authentication, a [body limit](../http-fundamentals/body-limit.md), and
[rate limiting](../abuse-prevention/rate-limiting.md). CORS does not replace CSRF
protection or authentication.

This handler factory is an application integration seam, not a standalone
unauthenticated production server. `User` is your existing authenticated user
type with a string `ID`. The `save` callback belongs to your application: it must
authorize ownership, enforce per-user registration quotas, and atomically upsert
the current browser's registration with a new version and its VAPID key identifier.
It must not trust client-supplied ownership. Import the packages shown here:

```go
import (
	"context"
	"net/http"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/push"
)

// registrationHandler accepts a bounded browser registration for the current user.
// Authentication and CSRF middleware must already protect the route; save owns
// authorization, quotas, persistence, and versioned replacement, not the driver.
func registrationHandler(save func(context.Context, string, push.Subscription) error) router.Handler {
	return func(writer http.ResponseWriter, incoming *http.Request) error {
		user, err := request.User[User](incoming)
		if err != nil {
			return err
		}
		subscription, err := request.JSON[push.Subscription](incoming, request.MaxSize(8<<10))
		if err != nil {
			return err
		}
		if err := push.NewMessage(subscription, "Registration check").Validate(); err != nil {
			http.Error(writer, "Invalid push subscription", http.StatusBadRequest)
			return nil
		}
		if err := save(incoming.Context(), user.ID, subscription); err != nil {
			return err
		}
		writer.WriteHeader(http.StatusCreated)
		return nil
	}
}
```

The temporary message validates the subscription's HTTPS shape and canonical
encryption keys without sending anything. JSON decoding deliberately ignores the
browser's extra `expirationTime` field. The Web Push driver additionally enforces
its endpoint-origin and public-network policy when sending. A registration record
does not bypass that policy or prove the user owns a particular physical device.

With an existing router `routes` that already applies session loading,
`csrf.Middleware()`, and `auth.Middleware(loginIdentity.Authenticator())`, mount the
handler on a route requiring your exact `User` type. Here `store` is your configured
`*cache.Cache`, `loginIdentity` is your shared `auth.NewSessionIdentity(users.Load)`
definition, and `saveSubscription` is the callback described above. Import
`arkoren.dev/core/http/auth`, `arkoren.dev/core/http/middleware/ratelimit`, and `time`:

```go
protected := routes.With(auth.Require[User](), ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
	limiter.Limit(10)
	limiter.Window(time.Minute)
}))
protected.Post("/push/subscriptions", registrationHandler(saveSubscription))
```

The handler reads at most 8 KiB. The route limiter adds an abuse boundary, but it
does not replace per-user storage quotas. Configure trusted proxy handling before
using an IP-based rate key, as explained in the rate-limiting guide. Protect
registration deletion with the same authentication, CSRF, and ownership checks.

## Sending and recognizing success

Construct the [Web Push sender](drivers.md#web-push), load only registrations
authorized for the order's customer, and send the
[order notification](overview.md#sending-a-notification). A nil send error is the
provider acceptance checkpoint. The “enabled” browser status means only that the
application stored the registration; the final checkpoint is a visible
notification and a click that opens the authorized order page.

Test these checkpoints separately using the [verification checklist](testing.md#verifying-a-browser).
For preference changes, call `subscription.unsubscribe()` in the browser and
remove the corresponding application record through your protected endpoint.
Decide explicitly whether registrations survive logout on a shared device; do not
silently move a previous user's registration to the next user who signs in.
Reconcile the current subscription on later signed-in visits rather than assuming
it never expires. None of this storage or routing is performed by the driver.

For the message and subscription types, see the [push API reference](https://pkg.go.dev/arkoren.dev/core/service/push).
