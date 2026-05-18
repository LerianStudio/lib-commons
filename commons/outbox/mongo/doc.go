// Package mongo provides MongoDB adapters for outbox repository contracts.
//
// The repository supports durable outbox persistence and dispatcher replay
// flows. It stores tenant IDs in a row-scoped BSON field by default, can use
// tenant-manager/core.ContextWithMB for tenant-scoped MongoDB databases, and can
// use WithTenantDatabaseResolver to let generic dispatcher loops resolve tenant
// databases from tenant IDs. SQL-style CreateWithTx accepts nil transactions for
// interface compatibility but does not join caller-supplied *sql.Tx values.
//
// MongoDB transaction atomicity flows through context: when ctx is a
// mongo.SessionContext inside a driver WithTransaction callback, Create joins
// that MongoDB transaction automatically because the underlying InsertOne uses
// the session-bound context directly.
package mongo
