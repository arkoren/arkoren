// Package azuresecret provides an Azure Key Vault [secret.Driver]. Missing
// secrets return [secret.ErrNotFound]. Write values must be valid UTF-8 text;
// explicitly encode binary data before writing. Invalid text fails locally
// rather than being replaced by native JSON encoding. Already-canceled calls
// fail before local or SDK validation.
//
// New uses the Azure default credential chain and has no optional settings.
// NewFrom borrows a configured native client without copying or closing it.
// Neither construction path provisions resources or tests permissions. Use the
// secret facade for typed reads and operation observations; native errors can
// retain provider diagnostics and must not be treated as sanitized log messages.
//
// Example:
//
//	driver, err := azuresecret.New(ctx, "https://example.vault.azure.net")
//	if err != nil {
//		return err
//	}
//	store := secret.New(driver)
//	_ = store
package azuresecret
