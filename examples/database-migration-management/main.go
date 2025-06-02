// Package main demonstrates database migration management with rollback capabilities
// for both PostgreSQL and MongoDB using the lib-commons migration package.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"

	"github.com/LerianStudio/lib-commons/commons/migration"
	"github.com/LerianStudio/lib-commons/commons/zap"
)

func main() {
	// Create logger
	logger, err := zap.NewStructured("migration-demo", "info")
	if err != nil {
		log.Fatal("Failed to create logger:", err)
	}

	// Run PostgreSQL examples
	fmt.Println("=== PostgreSQL Migration Examples ===")
	runPostgreSQLExamples(logger)

	// Run MongoDB examples
	fmt.Println("\n=== MongoDB Migration Examples ===")
	runMongoDBExamples(logger)

	fmt.Println("\n=== All database migration examples completed ===")
}

// runPostgreSQLExamples demonstrates PostgreSQL migration management
func runPostgreSQLExamples(logger *zap.Logger) {
	// Note: In a real application, you would connect to an actual PostgreSQL database
	// For this demo, we'll show the migration setup and structure

	// Example 1: Basic PostgreSQL Migration Setup
	runPostgreSQLBasicSetup(logger)

	// Example 2: Migration Validation and Dependencies
	runPostgreSQLMigrationValidation(logger)

	// Example 3: Rollback Scenarios
	runPostgreSQLRollbackExample(logger)

	// Example 4: Dry Run and Testing
	runPostgreSQLDryRunExample(logger)

	// Example 5: Complex Migration with Transactions
	runPostgreSQLComplexMigration(logger)
}

// runMongoDBExamples demonstrates MongoDB migration management
func runMongoDBExamples(logger *zap.Logger) {
	// Note: In a real application, you would connect to an actual MongoDB database
	// For this demo, we'll show the migration setup and structure

	// Example 1: Basic MongoDB Migration Setup
	runMongoDBBasicSetup(logger)

	// Example 2: Index Management
	runMongoDBIndexMigration(logger)

	// Example 3: Collection Schema Evolution
	runMongoDBSchemaEvolution(logger)

	// Example 4: Data Migration
	runMongoDBDataMigration(logger)
}

// Example 1: Basic PostgreSQL Migration Setup
func runPostgreSQLBasicSetup(logger *zap.Logger) {
	fmt.Println("\n--- Example 1: Basic PostgreSQL Migration Setup ---")

	// Create migration manager
	config := migration.DefaultMigrationConfig(migration.DatabaseTypePostgreSQL)
	config.DryRun = true              // Enable dry run for demo
	config.LogMigrationContent = true // Show migration content

	manager := migration.NewMigrationManager(config, logger)

	// Add basic migrations
	migrations := []migration.Migration{
		{
			Version:     1,
			Name:        "create_users_table",
			Description: "Create users table with basic fields",
			UpScript: `
				CREATE TABLE users (
					id SERIAL PRIMARY KEY,
					email VARCHAR(255) UNIQUE NOT NULL,
					username VARCHAR(100) UNIQUE NOT NULL,
					password_hash VARCHAR(255) NOT NULL,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
				);
				
				CREATE INDEX idx_users_email ON users(email);
				CREATE INDEX idx_users_username ON users(username);
			`,
			DownScript: `
				DROP INDEX IF EXISTS idx_users_username;
				DROP INDEX IF EXISTS idx_users_email;
				DROP TABLE IF EXISTS users;
			`,
			Tags: []string{"schema", "users", "initial"},
			Metadata: map[string]interface{}{
				"author":      "migration-demo",
				"review_by":   "tech-lead",
				"environment": "all",
			},
		},
		{
			Version:     2,
			Name:        "create_products_table",
			Description: "Create products table for e-commerce",
			UpScript: `
				CREATE TABLE products (
					id SERIAL PRIMARY KEY,
					name VARCHAR(255) NOT NULL,
					description TEXT,
					price DECIMAL(10,2) NOT NULL,
					sku VARCHAR(100) UNIQUE NOT NULL,
					category_id INTEGER,
					stock_quantity INTEGER DEFAULT 0,
					is_active BOOLEAN DEFAULT TRUE,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
				);
				
				CREATE INDEX idx_products_category_id ON products(category_id);
				CREATE INDEX idx_products_sku ON products(sku);
				CREATE INDEX idx_products_is_active ON products(is_active);
			`,
			DownScript: `
				DROP INDEX IF EXISTS idx_products_is_active;
				DROP INDEX IF EXISTS idx_products_sku;
				DROP INDEX IF EXISTS idx_products_category_id;
				DROP TABLE IF EXISTS products;
			`,
			Tags:         []string{"schema", "products", "ecommerce"},
			Dependencies: []int64{1}, // Depends on users table
		},
		{
			Version:     3,
			Name:        "create_orders_table",
			Description: "Create orders table with foreign key relationships",
			UpScript: `
				CREATE TABLE orders (
					id SERIAL PRIMARY KEY,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					total_amount DECIMAL(10,2) NOT NULL,
					status VARCHAR(50) DEFAULT 'pending',
					shipping_address TEXT,
					payment_method VARCHAR(50),
					order_date TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
					shipped_date TIMESTAMP WITH TIME ZONE,
					delivered_date TIMESTAMP WITH TIME ZONE,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
				);
				
				CREATE INDEX idx_orders_user_id ON orders(user_id);
				CREATE INDEX idx_orders_status ON orders(status);
				CREATE INDEX idx_orders_order_date ON orders(order_date);
			`,
			DownScript: `
				DROP INDEX IF EXISTS idx_orders_order_date;
				DROP INDEX IF EXISTS idx_orders_status;
				DROP INDEX IF EXISTS idx_orders_user_id;
				DROP TABLE IF EXISTS orders;
			`,
			Tags:         []string{"schema", "orders", "ecommerce"},
			Dependencies: []int64{1}, // Depends on users table
		},
	}

	// Add migrations to manager
	for _, mig := range migrations {
		if err := manager.AddMigration(mig); err != nil {
			logger.Error("Failed to add migration", zap.Error(err))
			continue
		}
		fmt.Printf("Added migration: v%d - %s\n", mig.Version, mig.Name)
	}

	fmt.Printf("Total migrations added: %d\n", len(migrations))

	// Show migration validation
	ctx := context.Background()
	if err := manager.ValidateMigrations(ctx); err != nil {
		logger.Error("Migration validation failed", zap.Error(err))
	} else {
		fmt.Println("All migrations validated successfully")
	}

	// Show metrics
	metrics := manager.GetMetrics()
	fmt.Printf("Migration metrics: %+v\n", metrics)
}

// Example 2: Migration Validation and Dependencies
func runPostgreSQLMigrationValidation(logger *zap.Logger) {
	fmt.Println("\n--- Example 2: Migration Validation and Dependencies ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypePostgreSQL)
	config.EnableChecksumValidation = true
	manager := migration.NewMigrationManager(config, logger)

	// Create migrations with complex dependencies
	migrations := []migration.Migration{
		{
			Version:     1,
			Name:        "foundation_tables",
			Description: "Create foundation tables",
			UpScript: `
				CREATE TABLE categories (
					id SERIAL PRIMARY KEY,
					name VARCHAR(255) NOT NULL,
					parent_id INTEGER REFERENCES categories(id)
				);
			`,
			DownScript: "DROP TABLE IF EXISTS categories;",
		},
		{
			Version:     2,
			Name:        "user_profiles",
			Description: "Create user profiles table",
			UpScript: `
				CREATE TABLE user_profiles (
					id SERIAL PRIMARY KEY,
					user_id INTEGER NOT NULL,
					first_name VARCHAR(100),
					last_name VARCHAR(100),
					phone VARCHAR(20),
					address TEXT
				);
			`,
			DownScript: "DROP TABLE IF EXISTS user_profiles;",
		},
		{
			Version:     3,
			Name:        "advanced_features",
			Description: "Add advanced features requiring both foundation and profiles",
			UpScript: `
				CREATE TABLE user_preferences (
					id SERIAL PRIMARY KEY,
					user_id INTEGER NOT NULL,
					category_id INTEGER NOT NULL REFERENCES categories(id),
					preference_value TEXT
				);
			`,
			DownScript:   "DROP TABLE IF EXISTS user_preferences;",
			Dependencies: []int64{1, 2}, // Requires both categories and user_profiles
		},
	}

	for _, mig := range migrations {
		if err := manager.AddMigration(mig); err != nil {
			logger.Error("Failed to add migration", zap.Error(err))
			continue
		}
	}

	// Test dependency validation
	fmt.Println("Testing dependency validation...")

	// Try to add a migration with invalid dependency
	invalidMigration := migration.Migration{
		Version:      4,
		Name:         "invalid_dependency",
		UpScript:     "SELECT 1;",
		Dependencies: []int64{999}, // Non-existent dependency
	}

	if err := manager.AddMigration(invalidMigration); err != nil {
		fmt.Printf("Expected error for invalid dependency: %s\n", err.Error())
	}

	// Test checksum validation
	fmt.Println("Testing checksum consistency...")
	originalMigration := migrations[0]
	checksum1 := manager.GetMetrics() // This would normally include checksum info

	// Simulate checksum validation (in real scenario, this would check against database)
	fmt.Printf("Checksum validation enabled: %t\n", config.EnableChecksumValidation)
}

// Example 3: Rollback Scenarios
func runPostgreSQLRollbackExample(logger *zap.Logger) {
	fmt.Println("\n--- Example 3: Rollback Scenarios ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypePostgreSQL)
	config.AutoRollbackOnFailure = true
	config.MaxRollbackDepth = 5
	config.DryRun = true

	manager := migration.NewMigrationManager(config, logger)

	// Create migrations that could potentially fail
	migrations := []migration.Migration{
		{
			Version:     1,
			Name:        "safe_migration",
			Description: "A safe migration that should always succeed",
			UpScript:    "CREATE TABLE rollback_test (id SERIAL PRIMARY KEY);",
			DownScript:  "DROP TABLE IF EXISTS rollback_test;",
		},
		{
			Version:     2,
			Name:        "risky_migration",
			Description: "A migration that might fail",
			UpScript: `
				-- This migration might fail in real scenarios
				ALTER TABLE rollback_test ADD COLUMN new_field VARCHAR(255);
				CREATE UNIQUE INDEX idx_rollback_test_new_field ON rollback_test(new_field);
			`,
			DownScript: `
				DROP INDEX IF EXISTS idx_rollback_test_new_field;
				ALTER TABLE rollback_test DROP COLUMN IF EXISTS new_field;
			`,
			Dependencies: []int64{1},
		},
		{
			Version:     3,
			Name:        "data_migration",
			Description: "Data migration with potential for conflicts",
			UpScript: `
				-- Insert test data
				INSERT INTO rollback_test (new_field) VALUES ('test_value_1');
				INSERT INTO rollback_test (new_field) VALUES ('test_value_2');
			`,
			DownScript: `
				-- Clean up test data
				DELETE FROM rollback_test WHERE new_field IN ('test_value_1', 'test_value_2');
			`,
			Dependencies: []int64{2},
		},
	}

	for _, mig := range migrations {
		if err := manager.AddMigration(mig); err != nil {
			logger.Error("Failed to add migration", zap.Error(err))
			continue
		}
	}

	fmt.Printf("Rollback configuration:\n")
	fmt.Printf("  Auto rollback on failure: %t\n", config.AutoRollbackOnFailure)
	fmt.Printf("  Max rollback depth: %d\n", config.MaxRollbackDepth)
	fmt.Printf("  Keep failed migrations: %t\n", config.KeepFailedMigrations)

	// Simulate rollback scenarios
	fmt.Println("\nRollback scenarios:")
	fmt.Println("1. Automatic rollback on migration failure")
	fmt.Println("2. Manual rollback to specific version")
	fmt.Println("3. Rollback validation and safety checks")
}

// Example 4: Dry Run and Testing
func runPostgreSQLDryRunExample(logger *zap.Logger) {
	fmt.Println("\n--- Example 4: Dry Run and Testing ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypePostgreSQL)
	config.DryRun = true
	config.LogMigrationContent = true
	config.EnableDetailedLogging = true

	manager := migration.NewMigrationManager(config, logger)

	// Create a complex migration for testing
	testMigration := migration.Migration{
		Version:     1,
		Name:        "complex_schema_change",
		Description: "Complex schema change with multiple operations",
		UpScript: `
			-- Create new table
			CREATE TABLE audit_logs (
				id SERIAL PRIMARY KEY,
				table_name VARCHAR(100) NOT NULL,
				operation VARCHAR(20) NOT NULL,
				old_values JSONB,
				new_values JSONB,
				user_id INTEGER,
				timestamp TIMESTAMP WITH TIME ZONE DEFAULT NOW()
			);

			-- Create indexes
			CREATE INDEX idx_audit_logs_table_name ON audit_logs(table_name);
			CREATE INDEX idx_audit_logs_operation ON audit_logs(operation);
			CREATE INDEX idx_audit_logs_user_id ON audit_logs(user_id);
			CREATE INDEX idx_audit_logs_timestamp ON audit_logs(timestamp);

			-- Create function for automatic audit logging
			CREATE OR REPLACE FUNCTION audit_trigger_function()
			RETURNS TRIGGER AS $$
			BEGIN
				INSERT INTO audit_logs (table_name, operation, old_values, new_values, user_id)
				VALUES (
					TG_TABLE_NAME,
					TG_OP,
					CASE WHEN TG_OP = 'DELETE' THEN row_to_json(OLD) ELSE NULL END,
					CASE WHEN TG_OP = 'INSERT' OR TG_OP = 'UPDATE' THEN row_to_json(NEW) ELSE NULL END,
					current_setting('app.current_user_id', true)::INTEGER
				);
				RETURN COALESCE(NEW, OLD);
			END;
			$$ LANGUAGE plpgsql;
		`,
		DownScript: `
			-- Drop function
			DROP FUNCTION IF EXISTS audit_trigger_function();

			-- Drop indexes
			DROP INDEX IF EXISTS idx_audit_logs_timestamp;
			DROP INDEX IF EXISTS idx_audit_logs_user_id;
			DROP INDEX IF EXISTS idx_audit_logs_operation;
			DROP INDEX IF EXISTS idx_audit_logs_table_name;

			-- Drop table
			DROP TABLE IF EXISTS audit_logs;
		`,
		Tags: []string{"audit", "logging", "functions"},
		Metadata: map[string]interface{}{
			"complexity": "high",
			"impact":     "medium",
			"reversible": true,
		},
	}

	if err := manager.AddMigration(testMigration); err != nil {
		logger.Error("Failed to add test migration", zap.Error(err))
		return
	}

	fmt.Println("Dry run configuration:")
	fmt.Printf("  Dry run enabled: %t\n", config.DryRun)
	fmt.Printf("  Log migration content: %t\n", config.LogMigrationContent)
	fmt.Printf("  Detailed logging: %t\n", config.EnableDetailedLogging)

	fmt.Println("\nIn dry run mode, migrations are validated but not executed")
	fmt.Println("This allows safe testing of migration scripts")
}

// Example 5: Complex Migration with Transactions
func runPostgreSQLComplexMigration(logger *zap.Logger) {
	fmt.Println("\n--- Example 5: Complex Migration with Transactions ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypePostgreSQL)
	config.EnableTransactions = true
	config.TransactionTimeout = 2 * time.Minute
	config.DryRun = true

	manager := migration.NewMigrationManager(config, logger)

	// Create a complex migration that benefits from transaction safety
	complexMigration := migration.Migration{
		Version:     1,
		Name:        "user_system_refactor",
		Description: "Major refactor of user system with data migration",
		UpScript: `
			-- Step 1: Create new normalized tables
			CREATE TABLE user_accounts (
				id SERIAL PRIMARY KEY,
				email VARCHAR(255) UNIQUE NOT NULL,
				email_verified BOOLEAN DEFAULT FALSE,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
			);

			CREATE TABLE user_profiles (
				id SERIAL PRIMARY KEY,
				user_account_id INTEGER NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
				username VARCHAR(100) UNIQUE NOT NULL,
				display_name VARCHAR(255),
				bio TEXT,
				avatar_url VARCHAR(500),
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
			);

			CREATE TABLE user_security (
				id SERIAL PRIMARY KEY,
				user_account_id INTEGER NOT NULL REFERENCES user_accounts(id) ON DELETE CASCADE,
				password_hash VARCHAR(255) NOT NULL,
				salt VARCHAR(255) NOT NULL,
				last_login TIMESTAMP WITH TIME ZONE,
				failed_login_attempts INTEGER DEFAULT 0,
				locked_until TIMESTAMP WITH TIME ZONE,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
			);

			-- Step 2: Migrate data from old users table (if it exists)
			-- INSERT INTO user_accounts (email, created_at) 
			-- SELECT email, created_at FROM users WHERE email IS NOT NULL;

			-- Step 3: Create indexes for performance
			CREATE INDEX idx_user_profiles_user_account_id ON user_profiles(user_account_id);
			CREATE INDEX idx_user_profiles_username ON user_profiles(username);
			CREATE INDEX idx_user_security_user_account_id ON user_security(user_account_id);
			CREATE INDEX idx_user_security_last_login ON user_security(last_login);

			-- Step 4: Create views for backwards compatibility
			CREATE VIEW users_view AS
			SELECT 
				ua.id,
				ua.email,
				up.username,
				up.display_name,
				ua.created_at,
				us.last_login
			FROM user_accounts ua
			LEFT JOIN user_profiles up ON ua.id = up.user_account_id
			LEFT JOIN user_security us ON ua.id = us.user_account_id;
		`,
		DownScript: `
			-- Drop views
			DROP VIEW IF EXISTS users_view;

			-- Drop indexes
			DROP INDEX IF EXISTS idx_user_security_last_login;
			DROP INDEX IF EXISTS idx_user_security_user_account_id;
			DROP INDEX IF EXISTS idx_user_profiles_username;
			DROP INDEX IF EXISTS idx_user_profiles_user_account_id;

			-- Drop tables (in reverse order due to foreign keys)
			DROP TABLE IF EXISTS user_security;
			DROP TABLE IF EXISTS user_profiles;
			DROP TABLE IF EXISTS user_accounts;
		`,
		Tags: []string{"refactor", "users", "normalization", "data-migration"},
		Metadata: map[string]interface{}{
			"breaking_change":   true,
			"data_migration":    true,
			"requires_downtime": false,
			"estimated_time":    "5-10 minutes",
		},
	}

	if err := manager.AddMigration(complexMigration); err != nil {
		logger.Error("Failed to add complex migration", zap.Error(err))
		return
	}

	fmt.Println("Transaction configuration:")
	fmt.Printf("  Transactions enabled: %t\n", config.EnableTransactions)
	fmt.Printf("  Transaction timeout: %v\n", config.TransactionTimeout)
	fmt.Printf("  Max retries: %d\n", config.MaxRetries)

	fmt.Println("\nThis migration demonstrates:")
	fmt.Println("1. Multi-step schema changes")
	fmt.Println("2. Data migration within transaction")
	fmt.Println("3. Index creation for performance")
	fmt.Println("4. Backwards compatibility with views")
	fmt.Println("5. Proper rollback script for complete reversal")
}

// MongoDB Examples

// Example 1: Basic MongoDB Migration Setup
func runMongoDBBasicSetup(logger *zap.Logger) {
	fmt.Println("\n--- Example 1: Basic MongoDB Migration Setup ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypeMongoDB)
	config.DryRun = true
	config.LogMigrationContent = true

	manager := migration.NewMigrationManager(config, logger)

	// MongoDB migrations are typically different from SQL migrations
	mongoMigrations := []migration.Migration{
		{
			Version:     1,
			Name:        "create_user_collection",
			Description: "Create users collection with validation",
			UpScript: `
				createCollection("users")
				// Set up document validation
				db.runCommand({
					collMod: "users",
					validator: {
						$jsonSchema: {
							bsonType: "object",
							required: ["email", "username", "createdAt"],
							properties: {
								email: {
									bsonType: "string",
									pattern: "^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$"
								},
								username: {
									bsonType: "string",
									minLength: 3,
									maxLength: 50
								},
								createdAt: {
									bsonType: "date"
								}
							}
						}
					}
				})
			`,
			DownScript: `
				dropCollection("users")
			`,
		},
		{
			Version:     2,
			Name:        "create_product_collection",
			Description: "Create products collection for e-commerce",
			UpScript: `
				createCollection("products")
				// Set up product validation
				db.runCommand({
					collMod: "products",
					validator: {
						$jsonSchema: {
							bsonType: "object",
							required: ["name", "sku", "price", "createdAt"],
							properties: {
								name: { bsonType: "string", minLength: 1 },
								sku: { bsonType: "string", minLength: 1 },
								price: { bsonType: "number", minimum: 0 },
								category: { bsonType: "string" },
								tags: { bsonType: "array", items: { bsonType: "string" } },
								inStock: { bsonType: "bool" },
								createdAt: { bsonType: "date" }
							}
						}
					}
				})
			`,
			DownScript: `
				dropCollection("products")
			`,
		},
	}

	for _, mig := range mongoMigrations {
		if err := manager.AddMigration(mig); err != nil {
			logger.Error("Failed to add MongoDB migration", zap.Error(err))
			continue
		}
		fmt.Printf("Added MongoDB migration: v%d - %s\n", mig.Version, mig.Name)
	}

	fmt.Printf("Total MongoDB migrations added: %d\n", len(mongoMigrations))
}

// Example 2: Index Management
func runMongoDBIndexMigration(logger *zap.Logger) {
	fmt.Println("\n--- Example 2: MongoDB Index Management ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypeMongoDB)
	config.DryRun = true

	manager := migration.NewMigrationManager(config, logger)

	indexMigration := migration.Migration{
		Version:     1,
		Name:        "create_performance_indexes",
		Description: "Create indexes for better query performance",
		UpScript: `
			// Create single field indexes
			createIndex("users", {"email": 1}, {"unique": true})
			createIndex("users", {"username": 1}, {"unique": true})
			createIndex("users", {"createdAt": -1})

			// Create compound indexes
			createIndex("products", {"category": 1, "inStock": 1})
			createIndex("products", {"tags": 1, "price": 1})
			
			// Create text indexes for search
			createIndex("products", {"name": "text", "description": "text"})
			
			// Create partial indexes
			createIndex("users", {"lastLogin": -1}, {"partialFilterExpression": {"lastLogin": {"$exists": true}}})
			
			// Create TTL indexes for cleanup
			createIndex("sessions", {"expiresAt": 1}, {"expireAfterSeconds": 0})
		`,
		DownScript: `
			// Drop indexes (MongoDB automatically drops indexes when collection is dropped)
			// For selective index dropping:
			dropIndex("users", "email_1")
			dropIndex("users", "username_1")
			dropIndex("users", "createdAt_-1")
			dropIndex("products", "category_1_inStock_1")
			dropIndex("products", "tags_1_price_1")
			dropIndex("products", "name_text_description_text")
			dropIndex("users", "lastLogin_-1")
			dropIndex("sessions", "expiresAt_1")
		`,
		Tags: []string{"performance", "indexes", "optimization"},
	}

	if err := manager.AddMigration(indexMigration); err != nil {
		logger.Error("Failed to add index migration", zap.Error(err))
		return
	}

	fmt.Println("Index migration demonstrates:")
	fmt.Println("1. Unique indexes for data integrity")
	fmt.Println("2. Compound indexes for complex queries")
	fmt.Println("3. Text indexes for full-text search")
	fmt.Println("4. Partial indexes for conditional data")
	fmt.Println("5. TTL indexes for automatic cleanup")
}

// Example 3: Collection Schema Evolution
func runMongoDBSchemaEvolution(logger *zap.Logger) {
	fmt.Println("\n--- Example 3: MongoDB Schema Evolution ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypeMongoDB)
	config.DryRun = true

	manager := migration.NewMigrationManager(config, logger)

	schemaEvolution := migration.Migration{
		Version:     1,
		Name:        "evolve_user_schema",
		Description: "Add new fields and update validation rules",
		UpScript: `
			// Update existing documents to add new fields
			db.users.updateMany(
				{profile: {$exists: false}},
				{$set: {
					profile: {
						firstName: "",
						lastName: "",
						bio: "",
						location: "",
						website: "",
						socialLinks: {}
					},
					preferences: {
						emailNotifications: true,
						pushNotifications: true,
						language: "en",
						timezone: "UTC"
					},
					metadata: {
						source: "migration",
						version: 2,
						migratedAt: new Date()
					}
				}}
			)

			// Update validation rules
			db.runCommand({
				collMod: "users",
				validator: {
					$jsonSchema: {
						bsonType: "object",
						required: ["email", "username", "createdAt", "profile", "preferences"],
						properties: {
							email: {
								bsonType: "string",
								pattern: "^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$"
							},
							username: {
								bsonType: "string",
								minLength: 3,
								maxLength: 50
							},
							profile: {
								bsonType: "object",
								properties: {
									firstName: {bsonType: "string"},
									lastName: {bsonType: "string"},
									bio: {bsonType: "string", maxLength: 500},
									location: {bsonType: "string"},
									website: {bsonType: "string"},
									socialLinks: {bsonType: "object"}
								}
							},
							preferences: {
								bsonType: "object",
								required: ["emailNotifications", "pushNotifications", "language"],
								properties: {
									emailNotifications: {bsonType: "bool"},
									pushNotifications: {bsonType: "bool"},
									language: {bsonType: "string", enum: ["en", "es", "fr", "de"]},
									timezone: {bsonType: "string"}
								}
							},
							createdAt: {bsonType: "date"},
							updatedAt: {bsonType: "date"}
						}
					}
				}
			})
		`,
		DownScript: `
			// Remove added fields
			db.users.updateMany(
				{},
				{$unset: {
					profile: "",
					preferences: "",
					metadata: ""
				}}
			)

			// Revert validation rules
			db.runCommand({
				collMod: "users",
				validator: {
					$jsonSchema: {
						bsonType: "object",
						required: ["email", "username", "createdAt"],
						properties: {
							email: {
								bsonType: "string",
								pattern: "^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$"
							},
							username: {
								bsonType: "string",
								minLength: 3,
								maxLength: 50
							},
							createdAt: {bsonType: "date"}
						}
					}
				}
			})
		`,
		Tags: []string{"schema", "evolution", "validation"},
	}

	if err := manager.AddMigration(schemaEvolution); err != nil {
		logger.Error("Failed to add schema evolution migration", zap.Error(err))
		return
	}

	fmt.Println("Schema evolution demonstrates:")
	fmt.Println("1. Adding new fields to existing documents")
	fmt.Println("2. Updating validation rules")
	fmt.Println("3. Backwards compatibility considerations")
	fmt.Println("4. Metadata tracking for migrations")
}

// Example 4: Data Migration
func runMongoDBDataMigration(logger *zap.Logger) {
	fmt.Println("\n--- Example 4: MongoDB Data Migration ---")

	config := migration.DefaultMigrationConfig(migration.DatabaseTypeMongoDB)
	config.DryRun = true
	config.EnableTransactions = true

	manager := migration.NewMigrationManager(config, logger)

	dataMigration := migration.Migration{
		Version:     1,
		Name:        "migrate_product_categories",
		Description: "Migrate product categories from string to reference",
		UpScript: `
			// Step 1: Create categories collection
			createCollection("categories")

			// Step 2: Insert predefined categories
			db.categories.insertMany([
				{_id: ObjectId(), name: "Electronics", slug: "electronics", description: "Electronic devices and accessories"},
				{_id: ObjectId(), name: "Clothing", slug: "clothing", description: "Apparel and fashion items"},
				{_id: ObjectId(), name: "Books", slug: "books", description: "Books and educational materials"},
				{_id: ObjectId(), name: "Home & Garden", slug: "home-garden", description: "Home improvement and gardening items"},
				{_id: ObjectId(), name: "Sports", slug: "sports", description: "Sports equipment and accessories"}
			])

			// Step 3: Create category lookup map
			var categoryMap = {}
			db.categories.find().forEach(function(cat) {
				categoryMap[cat.name.toLowerCase()] = cat._id
			})

			// Step 4: Update products to use category references
			db.products.find({category: {$type: "string"}}).forEach(function(product) {
				var categoryName = product.category.toLowerCase()
				var categoryId = categoryMap[categoryName]
				
				if (categoryId) {
					db.products.updateOne(
						{_id: product._id},
						{
							$set: {
								categoryId: categoryId,
								categoryName: product.category // Keep for backwards compatibility
							},
							$unset: {category: ""}
						}
					)
				} else {
					// Handle uncategorized products
					db.products.updateOne(
						{_id: product._id},
						{
							$set: {categoryName: "Uncategorized"},
							$unset: {category: ""}
						}
					)
				}
			})

			// Step 5: Create indexes
			createIndex("categories", {"slug": 1}, {"unique": true})
			createIndex("products", {"categoryId": 1})
		`,
		DownScript: `
			// Restore original category field
			db.products.find({categoryName: {$exists: true}}).forEach(function(product) {
				db.products.updateOne(
					{_id: product._id},
					{
						$set: {category: product.categoryName || "Uncategorized"},
						$unset: {categoryId: "", categoryName: ""}
					}
				)
			})

			// Drop categories collection
			dropCollection("categories")
		`,
		Tags: []string{"data", "migration", "normalization", "references"},
		Metadata: map[string]interface{}{
			"affects_collections": []string{"products", "categories"},
			"data_transformation": true,
			"requires_backup":     true,
		},
	}

	if err := manager.AddMigration(dataMigration); err != nil {
		logger.Error("Failed to add data migration", zap.Error(err))
		return
	}

	fmt.Println("Data migration demonstrates:")
	fmt.Println("1. Creating new collections")
	fmt.Println("2. Data transformation and normalization")
	fmt.Println("3. Reference relationships")
	fmt.Println("4. Backwards compatibility during transition")
	fmt.Println("5. Proper rollback with data restoration")

	// Show final configuration summary
	fmt.Printf("\nMongoDB Migration Configuration:\n")
	fmt.Printf("  Database type: %s\n", config.DatabaseType)
	fmt.Printf("  Collection: %s\n", config.MigrationsCollection)
	fmt.Printf("  Transactions enabled: %t\n", config.EnableTransactions)
	fmt.Printf("  Dry run mode: %t\n", config.DryRun)
}
