package permission_test

import (
	"context"
	"fmt"
	"log/slog"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"

	"arkoren.dev/ark/iam/permission"
)

func ExampleNewStore() {
	ctx := context.Background()
	driver, _ := sqlite.New(":memory:")
	_ = driver.Start(ctx)
	defer func() { _ = driver.Stop(ctx) }()
	db := database.New(driver)
	users := memoryauth.New()
	_ = users.Put(auth.UserRecord{Subject: "42"})
	provider, _ := auth.NewUserProvider(users)

	read := gate.NewPermission("posts:read")
	write := gate.NewPermission("posts:write")
	viewer := gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(read)
	})
	editor := gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(viewer).Grants(write)
	})

	permissions, err := permission.NewStore(provider, db, cache.New(memorycache.New()), func(policy *permission.Builder) {
		policy.Roles(editor)
	})
	if err != nil {
		fmt.Println(err)

		return
	}
	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Logger(slog.New(slog.DiscardHandler))
	})
	_ = migrator.Add(permissions.Migrations()...)
	_ = migrator.Up(ctx)

	_ = permissions.GrantRole(ctx, "42", editor)
	roles, scopes, _ := permissions.Load(ctx, "42")

	fmt.Println(roles, scopes)
	// Output: [editor viewer] [posts:read posts:write]
}
