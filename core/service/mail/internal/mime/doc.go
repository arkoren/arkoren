// Package mime renders validated mail projections without importing the mail
// facade. Its input deliberately has no BCC field. The caller owns message
// validation, envelope recipients, and the invalid-message error identity.
package mime
