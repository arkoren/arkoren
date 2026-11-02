---
title: Browser Integration
description: Register browser subscriptions for authenticated users and display submitted notifications with a service worker.
order: 40
summary:
  - Connect browser permission, service-worker display, and authenticated registration storage.
  - Save bounded subscription input with server-owned account and VAPID identity selection.
  - Handle click authorization, subscription changes, and shared-device ownership separately from acceptance.
---
# Browser Integration

To notify a customer outside an open page, connect three application-owned pieces:
a browser subscription, a stored registration for the signed-in customer, and a
service worker that displays incoming messages. The Web Push driver encrypts and
submits the message; it installs none of these pieces.

This walkthrough assumes an HTTPS application, an existing
[session login flow](../authentication/session-login.md), and a
[persistent VAPID identity](vapid-keys.md). Serve its public key as plain text at
`GET /push/public-key`. You will serve `/service-worker.js` and `/push.js` on
the same origin and implement `POST /push/subscriptions` in your application.

## Browser Requirements

Feature-detect service workers, `PushManager`, and `Notification`; do not choose
code by browser name. Request permission only in response to an explicit user
action, such as clicking “Enable order notifications.” The
[subscription API](https://developer.mozilla.org/en-US/docs/Web/API/PushManager/subscribe)
requires a secure context and documents the user-gesture requirement.

Chrome and Firefox support standards-based Web Push. Safari supports it starting
with Safari 16.1 on macOS Ventura. On iOS and iPadOS 16.4 or later, use an installed
Home Screen web app, not an ordinary browser tab. Supply a web app manifest with
`display: "standalone"` or `"fullscreen"` for that flow.
[WebKit's guidance](https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/)
also confirms that browser Web Push needs neither Apple Developer Program
membership nor native APNs credentials. Feature support is not proof that a
particular device will display your notification.

## Displaying Notifications

Serve the following JavaScript at `/service-worker.js`; its root location permits
root scope. The driver's decrypted application payload is flat JSON, with no
`notification` wrapper:

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

`title` is always present; empty body and data fields are omitted. Display that
shape with `showNotification`. Use `waitUntil` to extend the worker event while
the display promise completes:

```js
self.addEventListener("push", (event) => {
  event.waitUntil((async () => {
    let payload = { title: "Order update" };
    try {
      const decoded = event.data?.json();
      if (decoded && typeof decoded.title === "string") payload = decoded;
    } catch {
      // Malformed data still gets a visible fallback notification.
    }
    await self.registration.showNotification(payload.title || "Order update", {
      body: payload.body,
      data: payload.data,
    });
  })());
});
```

Notifications can appear on a locked device, so avoid sensitive display text.
Application data is navigation input, not permission to read an order. Add a
click handler to the same worker file that permits only your own origin:

```js
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  let target = new URL("/", self.location.origin);
  try {
    const requested = new URL(event.notification.data?.url ?? "/", self.location.origin);
    if (requested.origin === self.location.origin) target = requested;
  } catch {
    // Invalid URLs fall back to the application's home page.
  }
  event.waitUntil(self.clients.openWindow(target.href));
});
```

The destination page must still authenticate the viewer and authorize access to
the order. Cross-origin or malformed URLs open the home page instead.

## Preparing Registration

On the signed-in page, add a disabled button with `id="enable-push"` and a status
element with `id="push-status"`. Load `/push.js` with
`<script type="module" src="/push.js"></script>`. The following blocks form that
module in order.

First decode the public base64url point—not the private key—and prepare the worker
and configuration before the button can request permission:

```js
function publicKeyBytes(encoded) {
  const text = encoded.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(text + "=".repeat((4 - text.length % 4) % 4));
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

async function preparePush() {
  if (!("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window)) {
    throw new Error("Push is unavailable in this browser context.");
  }
  await navigator.serviceWorker.register("/service-worker.js");
  const registration = await navigator.serviceWorker.ready;
  const response = await fetch("/push/public-key", { credentials: "same-origin" });
  if (!response.ok) throw new Error("Push configuration is unavailable.");
  return { registration, applicationServerKey: publicKeyBytes((await response.text()).trim()) };
}
```

Preparation creates no subscription and does not request notification permission.
Leave the button disabled if it fails, and show a fixed status instead of logging
native errors or subscription material:

```js
const button = document.querySelector("#enable-push");
const status = document.querySelector("#push-status");
let configuration;
button.disabled = true;
try {
  configuration = await preparePush();
  button.disabled = false;
} catch {
  status.textContent = "Notifications are unavailable. Check browser support and configuration.";
}
```

## Saving Subscriptions

Before enabling uploads, protect the registration route as described in
[Registration Ownership](#registration-ownership). Reusing a subscription here
assumes the same persisted VAPID key; after a key change, use the explicit
[rotation flow](vapid-keys.md#rotating-identity) instead.

Add this click handler to `/push.js`. Only a successful server response produces
the “enabled” status:

```js
button.addEventListener("click", async () => {
  if (!configuration) return;
  button.disabled = true;
  try {
    const permission = await Notification.requestPermission();
    if (permission !== "granted") {
      status.textContent = "Notifications were not enabled. Check browser permissions.";
      return;
    }
    const { registration, applicationServerKey } = configuration;
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
    if (!saved.ok) throw new Error("Registration was not saved.");
    status.textContent = "Order notifications are enabled for this browser.";
  } catch {
    status.textContent = "Could not enable notifications. Try again from this page.";
  } finally {
    button.disabled = false;
  }
});
```

`userVisibleOnly: true` commits this subscription to visible notifications, not
silent background jobs. `toJSON()` supplies `endpoint` and `keys`, with `p256dh`
and `auth`; browsers can also include `expirationTime`. A failed upload can leave
a browser subscription without a stored application registration. A later
explicit attempt reuses it rather than claiming that preferences were saved.

## Registration Ownership

The server must derive the owner from authentication, never from JSON. Configure
[session loading](../http-capabilities/sessions.md),
[CSRF protection](../browser-security/csrf-protection.md), authentication, a
[body limit](../http-fundamentals/body-limit.md), JSON
[content-type policy](../http-fundamentals/content-type.md), and
[rate limiting](../abuse-prevention/rate-limiting.md) before mounting the route.
CORS alone supplies neither authentication nor CSRF protection.

Your storage callback owns authorization, per-user quotas, persistence, and atomic
versioned replacement. For this example, define a server-side draft using
`arkoren.dev/core/service/push`:

```go
type RegistrationDraft struct {
	OwnerID      string
	VAPIDKeyID   string
	Subscription push.Subscription
}
```

`OwnerID` comes from your authenticated `User` value; `VAPIDKeyID` comes from server
configuration matching the served public key. Storage assigns a registration ID
and a new version. Enforce endpoint uniqueness and reject an endpoint already
owned by another account; do not silently transfer ownership when someone signs
in on a shared browser. A subscription is a sensitive capability, not proof of
physical-device ownership.

The following complete handler factory uses your existing `User` type with a
string `ID`. Import `context`, `net/http`, `arkoren.dev/core/http/request`,
`arkoren.dev/core/http/router`, and `arkoren.dev/core/service/push`. The `save`
callback implements the storage contract above:

```go
func registrationHandler(keyID string, save func(context.Context, RegistrationDraft) error) router.Handler {
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
		draft := RegistrationDraft{OwnerID: user.ID, VAPIDKeyID: keyID, Subscription: subscription}
		if err := save(incoming.Context(), draft); err != nil {
			return err
		}
		writer.WriteHeader(http.StatusCreated)
		return nil
	}
}
```

This handler applies an 8 KiB body limit. Default JSON decoding ignores the browser's
extra `expirationTime`; it is not part of `push.Subscription` and does not promise
an unlimited lifetime. Structural validation checks the HTTPS endpoint and
canonical encryption keys without sending a notification. `p256dh` is an
unpadded base64url encoding of a valid 65-byte uncompressed P-256 public point;
`auth` encodes exactly 16 bytes. These are browser subscription keys, not the
server's VAPID signing key. Endpoints cannot contain URL credentials or a literal
`#`, even an empty trailing fragment; encoded `%23` remains opaque path/query data.

With your already-protected router named `protected`, your server-selected
`keyID`, and application callback `saveRegistration`, mount the handler:

```go
protected.Post("/push/subscriptions", registrationHandler(keyID, saveRegistration))
```

The route's authentication must load the exact `User` type read by the handler.
Rate limiting bounds requests, not stored device counts; storage quotas remain
necessary. Keep registration endpoints and auth secrets out of logs. Never use
client input to configure allowed origins: a valid stored subscription remains
subject to the driver's [SSRF and public-network policy](drivers.md#endpoint-policy)
at send time.

## Sending Updates

Load only registrations authorized for the order's customer, select their matching
VAPID signer, and submit the [order notice](overview.md#preparing-messages).
The UI's “enabled” status means storage succeeded. A nil send error means provider
acceptance. A visible notification is a third checkpoint, verified separately
with the [browser checklist](testing.md#verifying-browsers).

Browser permission, subscriptions, and server storage have separate lifetimes.
On preference removal, stop targeting the server registration and call
`subscription.unsubscribe()` in the browser, handling its result; protect the
application deletion route with authentication, CSRF, and ownership checks.
Neither action recalls an already submitted notification.

Decide whether registrations survive logout on a shared device. Invalidate stale
page UI on account changes and do not reassign the old account's endpoint to the
new account implicitly. Reconcile subscription state on later signed-in visits:
permission can be revoked and subscriptions can expire or change. A confirmed
not-found send invalidates only the submitted stored version; authentication
failures and uncertain outcomes do not justify registration deletion.

For the message and subscription types, see the
[push API reference](https://pkg.go.dev/arkoren.dev/core/service/push).
