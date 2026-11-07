## Summary

<!-- What does this change, and why? Link the issue it resolves, for example "Closes #123". -->

## Affected modules

<!-- For example: core, driver/redis, cmd/arkoren, website. -->

## Checklist

- [ ] Tests cover the change and fail without it.
- [ ] `make check MODULE=...` passes for each changed module.
- [ ] Go doc comments and consumer guides describe any change in behavior.
- [ ] Contract changes update the `<contract>test` suite, the memory driver, and every vendor driver.
- [ ] Driver behavior changes update the service's compatibility matrix.
- [ ] After guide changes, I ran `make generate` and `make docs`.
- [ ] Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/), and breaking changes include a `BREAKING CHANGE:` footer.

<!-- Remove checklist items that do not apply. -->
