# Examples

A small shop service shaped by the arkoren-structure rules: a `server` with HTTP routes and an event listener, a `worker` consuming jobs, and a `migrate` one-shot. Every file compiles and passes `go vet` and `arkoren check` against `arkoren.dev/core` v0.17.0, and the test at the end passes. Drivers are SQLite and the memory queue; swap them inside the bootstrap providers without touching features.

## Contents

- Bootstrap providers: database (two singletons, one constructor), queue (driver, dispatcher, consumer, runner), router, migrator
- Migrations: schema-builder operations extracted into exported variables
- Feature root: declarations, providers, a store with exported query functions, a service with an extracted transaction body and an after-commit event
- Web surface: routes, handlers using the request and response helpers, a validation schema, problems
- Other surfaces: job handlers, an event listener in another feature
- Processes: thin `main` functions and the `Compose` of each process in `internal/bootstrap/<process>`
- Test: composing surfaces and recording a request

## Bootstrap providers

Providers `Use` their dependencies first, then construct and register lifecycle. `NewDatabase` opens a new pool on every call, so only providers call it. The generated `Router` also registers security headers, telemetry, CSRF and probes; it is trimmed here.

`internal/bootstrap/database.go`

```go
package bootstrap

import (
	"os"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/database"
	"arkoren.dev/driver/sqlite/sqlitedatabase"
)

// Database is the process's primary database.
var Database = application.Provide(func(compose *application.Composer) *database.Database {
	return NewDatabase(compose, "DATABASE_URL")
})

// ReportingDatabase is a second singleton of the same kind, sharing NewDatabase.
var ReportingDatabase = application.Provide(func(compose *application.Composer) *database.Database {
	return NewDatabase(compose, "REPORTING_DATABASE_URL")
})

// NewDatabase opens a new pool from the named variable and registers its lifecycle.
// Each call is a separate pool; call it only from providers.
func NewDatabase(compose *application.Composer, variable string) *database.Database {
	driver := compose.Must(sqlitedatabase.New(os.Getenv(variable)))
	compose.Cleanup(driver.Stop)
	compose.Startup(driver.Start)

	return database.New(driver)
}
```

`internal/bootstrap/queue.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
)

// Queue is the process's queue driver.
var Queue = application.Provide(func(compose *application.Composer) *memoryqueue.Driver {
	driver := memoryqueue.New()
	compose.Cleanup(driver.Stop)
	compose.Startup(driver.Start)

	return driver
})

// Dispatcher sends jobs through Queue.
var Dispatcher = application.Provide(func(compose *application.Composer) *queue.Dispatcher {
	driver := compose.Use(Queue)

	return queue.NewDispatcher(driver)
})

// Consumer receives jobs from Queue. Feature jobs packages register handlers on it.
var Consumer = application.Provide(func(compose *application.Composer) *queue.Consumer {
	driver := compose.Use(Queue)

	return queue.NewConsumer(driver)
})

// Work runs Consumer. Call it last in a worker's Compose, after every handler is registered.
func Work(compose *application.Composer) {
	consumer := compose.Use(Consumer)

	compose.Run(consumer.Run)
}
```

`internal/bootstrap/router.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"
)

// Router is the process's shared router.
var Router = application.Provide(func(compose *application.Composer) *router.Router {
	routes := router.New()
	routes.Use(router.Recover())

	return routes
})
```

`internal/bootstrap/migrator.go`

```go
package bootstrap

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/database/migrate"

	"example.com/shop/internal/migrations"
)

// Migrator registers the application's migrations (and each ark's Migrations()).
var Migrator = application.Provide(func(compose *application.Composer) *migrate.Migrator {
	db := compose.Use(Database)

	migrator := migrate.New(db)
	compose.Require(migrator.Add(migrations.All()...))

	return migrator
})

// Migrate runs pending migrations once; the process ends when Up returns.
func Migrate(compose *application.Composer) {
	migrator := compose.Use(Migrator)

	compose.Run(migrator.Up)
}
```


## Migrations

Each migration has its own numbered file (`arkoren add migration`). Schema changes are schema-builder operations in exported variables, never dialect SQL, and the migration only references them.

`internal/migrations/migrations.go`

```go
package migrations

import "arkoren.dev/core/service/database/migrate"

// All returns the application's migrations in order.
func All() []migrate.Definition {
	return []migrate.Definition{CreateOrders}
}
```

`internal/migrations/0001_create_orders.go`

```go
package migrations

import (
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query/schema"
)

// OrdersTable creates the orders table.
var OrdersTable = schema.Create("orders", func(table *schema.Table) {
	table.ID()
	table.String("sku", 64)
	table.BigInteger("total")
	table.Timestamps()
})

// DropOrdersTable reverses OrdersTable and destroys its data.
var DropOrdersTable = schema.Drop("orders")

// CreateOrders applies OrdersTable.
var CreateOrders = migrate.NewMigration("create-orders", func(migration *migrate.MigrationBuilder) {
	migration.Up(migrate.Schema(OrdersTable))
	migration.Down(migrate.Schema(DropOrdersTable))
})
```


## Feature root

The root declares what others listen to or dispatch, because the root cannot import its own children. Statements are built by exported query functions that Store methods only run; the function passed to `WithTransaction` is an exported method too. `catalog` is a second feature whose service satisfies the interface `orders` defines.

`internal/orders/orders.go`

```go
package orders

import (
	"errors"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/queue"
)

// Placement describes an order to place.
type Placement struct {
	SKU   string `json:"sku"`
	Total int64  `json:"total"`
}

// ErrOutOfStock reports an unavailable SKU. Surfaces map it to their own errors.
var ErrOutOfStock = errors.New("orders: out of stock")

// Placed is emitted after an order commits. Declared in the root so any package may listen.
var Placed = event.NewTopic[Placement]("orders.placed")

// Receipt is the payload of SendReceipt.
type Receipt struct {
	SKU string `json:"sku"`
}

// SendReceipt is declared in the root so the service (or another feature) can dispatch it;
// the jobs child only handles it.
var SendReceipt = queue.NewJob[Receipt]("orders.send-receipt")
```

`internal/orders/feature.go`

```go
package orders

import "arkoren.dev/core/application"

// DefaultStore is the process's orders store.
var DefaultStore = application.Provide(NewStore)

// DefaultService is the process's orders service.
var DefaultService = application.Provide(NewService)
```

`internal/orders/store.go`

```go
package orders

import (
	"context"

	"arkoren.dev/core/application"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"example.com/shop/internal/bootstrap"
)

// Store persists orders.
type Store struct {
	db *database.Database
}

// NewStore builds the store over the shared database.
func NewStore(compose *application.Composer) *Store {
	db := compose.Use(bootstrap.Database)

	return &Store{db: db}
}

// InsertOrder is the statement that stores placement.
func InsertOrder(placement Placement) query.Query {
	return query.Table("orders", func(statement *query.Builder) {
		statement.Insert(map[string]any{"sku": placement.SKU, "total": placement.Total})
	})
}

// Create stores placement. It joins a transaction carried by ctx.
func (store *Store) Create(ctx context.Context, placement Placement) error {
	_, err := store.db.Exec(ctx, InsertOrder(placement))

	return err
}
```

`internal/orders/service.go`

```go
package orders

import (
	"context"

	"arkoren.dev/core/application"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"

	"example.com/shop/internal/bootstrap"
	"example.com/shop/internal/catalog"
)

// Availability is what orders needs from another feature; orders defines it.
type Availability interface {
	Available(ctx context.Context, sku string) (bool, error)
}

// Service places orders.
type Service struct {
	db     *database.Database
	store  *Store
	stock  Availability
	events *event.Bus
}

// NewService wires the service from providers during composition.
func NewService(compose *application.Composer) *Service {
	db := compose.Use(bootstrap.Database)
	store := compose.Use(DefaultStore)
	stock := compose.Use(catalog.DefaultService)
	events := compose.App().Events()

	return &Service{db: db, store: store, stock: stock, events: events}
}

// Place checks stock, then runs Record in one transaction.
func (service *Service) Place(ctx context.Context, placement Placement) error {
	available, err := service.stock.Available(ctx, placement.SKU)
	if err != nil {
		return err
	}

	if !available {
		return ErrOutOfStock
	}

	return service.db.WithTransaction(ctx, service.Record(placement))
}

// Record is the unit of work Place runs in a transaction: store the order and
// emit Placed once it commits.
func (service *Service) Record(placement Placement) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := service.store.Create(ctx, placement); err != nil {
			return err
		}

		return service.db.AfterCommit(ctx, service.Announce(placement))
	}
}

// Announce emits Placed for placement.
func (service *Service) Announce(placement Placement) func(context.Context) error {
	return func(ctx context.Context) error {
		service.events.Emit(ctx, Placed, placement)

		return nil
	}
}
```

`internal/catalog/catalog.go`

```go
package catalog

import (
	"context"

	"arkoren.dev/core/application"
)

// DefaultService is the process's catalog service.
var DefaultService = application.Provide(NewService)

// Service answers stock questions.
type Service struct{}

// NewService builds the catalog service.
func NewService(compose *application.Composer) *Service {
	return &Service{}
}

// Available reports whether sku is in stock.
func (service *Service) Available(ctx context.Context, sku string) (bool, error) {
	return sku != "none", nil
}
```


## Web surface

The surface owns its wire concerns: handlers read with `request.ParseJSON` against a schema in `schemas.go` (invalid input becomes a 422 problem) and write with `response.JSON`; `problems.go` maps the root's plain domain errors to problem details for every route of the group.

`internal/orders/web/routes.go`

```go
package web

import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"

	"example.com/shop/internal/bootstrap"
	"example.com/shop/internal/orders"
)

// Routes mounts the orders endpoints on the shared router.
func Routes(compose *application.Composer) {
	routes := compose.Use(bootstrap.Router)
	service := compose.Use(orders.DefaultService)

	routes.Group("/orders", func(group *router.Router) {
		group.Use(Problems)
		group.Post("/", Place(service))
	})
}
```

`internal/orders/web/handlers.go`

```go
package web

import (
	"net/http"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"

	"example.com/shop/internal/orders"
)

// Place handles POST /orders. Invalid input becomes a 422 problem through
// request.ParseJSON; domain errors become problems through Problems.
func Place(service *orders.Service) router.Handler {
	return func(writer http.ResponseWriter, r *http.Request) error {
		placement, err := request.ParseJSON(r, PlacementSchema)
		if err != nil {
			return err
		}

		if err := service.Place(r.Context(), placement); err != nil {
			return err
		}

		return response.JSON(writer, http.StatusCreated, placement)
	}
}
```

`internal/orders/web/schemas.go`

```go
package web

import (
	"arkoren.dev/foundation/validate"

	"example.com/shop/internal/orders"
)

// PlacementSchema validates and normalizes the POST /orders body.
var PlacementSchema = validate.Struct(func(object *validate.StructBuilder[orders.Placement]) {
	object.Field("SKU", func(sku *validate.StringBuilder[string]) {
		sku.Trim()
		sku.Required()
		sku.Max(64)
	})
	object.Field("Total", func(total *validate.IntBuilder[int64]) {
		total.Required()
		total.Min(1)
	})
})
```

`internal/orders/web/problems.go`

```go
package web

import (
	"net/http"

	"arkoren.dev/core/http/middleware/errormap"
	"arkoren.dev/foundation/problem"

	"example.com/shop/internal/orders"
)

// OutOfStock is the problem clients receive when a SKU is unavailable.
var OutOfStock = problem.New(func(details *problem.Builder) {
	details.Type("https://shop.example/problems/out-of-stock")
	details.Title("Out of stock")
	details.Status(http.StatusConflict)
})

// Problems maps orders domain errors to their problems for every orders route.
var Problems = errormap.Middleware(
	errormap.Map(orders.ErrOutOfStock, OutOfStock),
)
```


## Other surfaces

Each surface exports one registration function. The listener lives in the subscribing feature and is registered by the server, the process that emits `orders.Placed`.

`internal/orders/jobs/handlers.go`

```go
package jobs

import (
	"context"

	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
	"example.com/shop/internal/orders"
)

// Handlers registers the orders job handlers on the shared consumer.
func Handlers(compose *application.Composer) {
	consumer := compose.Use(bootstrap.Consumer)
	logger := compose.App().Logger()

	compose.Require(consumer.Handle(orders.SendReceipt, func(ctx context.Context, receipt orders.Receipt) error {
		logger.InfoContext(ctx, "receipt sent", "sku", receipt.SKU)

		return nil
	}))
}
```

`internal/notifications/events/listeners.go`

```go
package events

import (
	"context"

	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
	"example.com/shop/internal/orders"
)

// Listeners subscribes notifications to other features' events. The bus is
// in-process: call it from the Compose of the process that emits the event.
func Listeners(compose *application.Composer) {
	events := compose.App().Events()
	dispatcher := compose.Use(bootstrap.Dispatcher)

	compose.MustDefer(events.Listen(orders.Placed, func(ctx context.Context, placement orders.Placement) error {
		return dispatcher.Dispatch(ctx, orders.SendReceipt, orders.Receipt{SKU: placement.SKU})
	}))
}
```


## Processes

`main` only builds and runs the application; each process's `Compose` lives in `internal/bootstrap/<process>`, which may import feature surfaces while `internal/bootstrap` itself never does. Generated `main` functions also load `.env` and apply `bootstrap.Application()` settings; they are trimmed here.

`cmd/server/main.go`

```go
package main

import (
	"context"
	"os"

	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap/server"
)

func main() {
	app := application.New()

	ctx, stop := application.NewSignalContext(context.Background())
	defer stop()

	if app.Run(ctx, server.Compose) != nil {
		os.Exit(1)
	}
}
```

`internal/bootstrap/server/compose.go`

```go
package server

import (
	"arkoren.dev/core/application"

	notificationsevents "example.com/shop/internal/notifications/events"
	ordersweb "example.com/shop/internal/orders/web"
)

// Compose is the explicit list of what the server runs.
func Compose(compose *application.Composer) {
	ordersweb.Routes(compose)
	notificationsevents.Listeners(compose)
}
```

`internal/bootstrap/worker/compose.go`

```go
package worker

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
	ordersjobs "example.com/shop/internal/orders/jobs"
)

// Compose registers job handlers, then runs the consumer.
func Compose(compose *application.Composer) {
	ordersjobs.Handlers(compose)

	bootstrap.Work(compose)
}
```

`internal/bootstrap/migrate/compose.go`

```go
package migrate

import (
	"arkoren.dev/core/application"

	"example.com/shop/internal/bootstrap"
)

// Compose runs pending migrations; the process exits when they finish.
func Compose(compose *application.Composer) {
	bootstrap.Migrate(compose)
}
```


## Test

Start only the registration functions under test, migrate an in-memory database in a `Startup` callback, and record requests on the shared router.

`internal/orders/web/routes_test.go`

```go
package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/router"

	"example.com/shop/internal/bootstrap"
	notificationsevents "example.com/shop/internal/notifications/events"
	ordersweb "example.com/shop/internal/orders/web"
)

func TestPlaceOrder(t *testing.T) {
	routes := start(t)

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"created", `{"sku":" tea ","total":300}`, http.StatusCreated},
		{"invalid", `{"sku":"","total":0}`, http.StatusUnprocessableEntity},
		{"out of stock", `{"sku":"none","total":300}`, http.StatusConflict},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := routes.Record(httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(test.body)))
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
		})
	}
}

// start composes only the registrations under test over an in-memory database.
func start(t *testing.T) *router.Router {
	t.Helper()
	t.Setenv("DATABASE_URL", "file::memory:")

	app := application.New()

	var routes *router.Router

	err := app.Start(t.Context(), func(compose *application.Composer) {
		ordersweb.Routes(compose)
		notificationsevents.Listeners(compose)

		routes = compose.Use(bootstrap.Router)
		migrator := compose.Use(bootstrap.Migrator)

		compose.Startup(migrator.Up)
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})

	return routes
}
```

