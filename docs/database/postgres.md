# PostgreSQL Integration

The PostgreSQL package provides connection management, query helpers, and utilities for working with PostgreSQL databases.

## Table of Contents

- [Overview](#overview)
- [Connection Management](#connection-management)
- [Query Builders](#query-builders)
- [Pagination](#pagination)
- [Transactions](#transactions)
- [Migrations](#migrations)
- [Best Practices](#best-practices)

## Overview

The PostgreSQL integration provides:

- Connection pooling and management
- Query building helpers
- Pagination support
- Transaction management
- Migration support
- Prepared statement caching
- Context-aware operations

## Connection Management

### Basic Connection

```go
import (
    "github.com/yourusername/commons-go/commons/postgres"
)

// Create connection from environment
conn, err := postgres.Connect()
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

// Get underlying database connection
db := conn.ConnectionDB
```

### Connection with Configuration

```go
config := postgres.Config{
    Host:            "localhost",
    Port:            5432,
    User:            "postgres",
    Password:        "password",
    Database:        "mydb",
    SSLMode:         "disable",
    MaxConnections:  25,
    MaxIdleConns:    5,
    ConnMaxLifetime: 5 * time.Minute,
}

conn, err := postgres.ConnectWithConfig(config)
if err != nil {
    log.Fatal(err)
}
```

### Connection Pool Management

```go
// Check connection health
ctx := context.Background()
if err := conn.Ping(ctx); err != nil {
    log.Printf("Database connection lost: %v", err)
}

// Get pool statistics
stats := conn.Stats()
log.Printf("Open connections: %d", stats.OpenConnections)
log.Printf("In use: %d", stats.InUse)
log.Printf("Idle: %d", stats.Idle)
```

## Query Builders

### Select Queries

```go
// Simple query
query := postgres.NewQueryBuilder().
    Select("id", "name", "email").
    From("users").
    Where("status = ?", "active").
    OrderBy("created_at DESC").
    Limit(10)

sql, args := query.Build()
// SQL: SELECT id, name, email FROM users WHERE status = $1 ORDER BY created_at DESC LIMIT 10

rows, err := db.Query(sql, args...)
```

### Complex Queries

```go
// Query with joins and conditions
query := postgres.NewQueryBuilder().
    Select("u.id", "u.name", "COUNT(o.id) as order_count").
    From("users u").
    LeftJoin("orders o ON o.user_id = u.id").
    Where("u.created_at >= ?", startDate).
    Where("u.status = ?", "active").
    GroupBy("u.id", "u.name").
    Having("COUNT(o.id) > ?", 5).
    OrderBy("order_count DESC").
    Limit(20)

sql, args := query.Build()
```

### Insert Queries

```go
// Single insert
query := postgres.NewQueryBuilder().
    Insert("users").
    Columns("name", "email", "created_at").
    Values("John Doe", "john@example.com", time.Now()).
    Returning("id")

var userID int
err := db.QueryRow(query.Build()).Scan(&userID)
```

### Update Queries

```go
query := postgres.NewQueryBuilder().
    Update("users").
    Set("name", "Jane Doe").
    Set("updated_at", time.Now()).
    Where("id = ?", userID).
    Returning("updated_at")

var updatedAt time.Time
err := db.QueryRow(query.Build()).Scan(&updatedAt)
```

### Delete Queries

```go
query := postgres.NewQueryBuilder().
    Delete("users").
    Where("status = ?", "inactive").
    Where("created_at < ?", cutoffDate)

result, err := db.Exec(query.Build())
affected, _ := result.RowsAffected()
log.Printf("Deleted %d inactive users", affected)
```

## Pagination

### Basic Pagination

```go
// Create pagination request
page := &postgres.Pagination{
    Page:    1,
    Limit:   20,
    OrderBy: "created_at DESC",
}

// Apply to query
query := postgres.NewQueryBuilder().
    Select("*").
    From("products").
    Where("status = ?", "active")

// Get paginated results
var products []Product
total, err := postgres.Paginate(db, query, page, &products)
if err != nil {
    log.Fatal(err)
}

log.Printf("Found %d products (page %d of %d)", 
    len(products), page.Page, page.TotalPages(total))
```

### Cursor-Based Pagination

```go
type CursorPagination struct {
    Cursor string
    Limit  int
}

func GetProductsAfterCursor(cursor string, limit int) ([]Product, string, error) {
    query := postgres.NewQueryBuilder().
        Select("*").
        From("products").
        Where("id > ?", cursor).
        OrderBy("id ASC").
        Limit(limit + 1) // Get one extra to determine if there's a next page
    
    rows, err := db.Query(query.Build())
    if err != nil {
        return nil, "", err
    }
    defer rows.Close()
    
    var products []Product
    for rows.Next() {
        var p Product
        err := rows.Scan(&p.ID, &p.Name, &p.Price)
        if err != nil {
            return nil, "", err
        }
        products = append(products, p)
    }
    
    var nextCursor string
    if len(products) > limit {
        // Remove extra item and set cursor
        products = products[:limit]
        nextCursor = products[len(products)-1].ID
    }
    
    return products, nextCursor, nil
}
```

## Transactions

### Basic Transactions

```go
// Start transaction
tx, err := db.BeginTx(ctx, nil)
if err != nil {
    return err
}
defer tx.Rollback() // Will be no-op if committed

// Execute queries
_, err = tx.Exec("INSERT INTO accounts (name) VALUES ($1)", name)
if err != nil {
    return err
}

_, err = tx.Exec("UPDATE balances SET amount = amount + $1 WHERE account_id = $2", amount, accountID)
if err != nil {
    return err
}

// Commit transaction
return tx.Commit()
```

### Transaction Helper

```go
// Transaction wrapper function
func WithTransaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
    tx, err := db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    
    defer func() {
        if p := recover(); p != nil {
            tx.Rollback()
            panic(p)
        }
    }()
    
    err = fn(tx)
    if err != nil {
        tx.Rollback()
        return err
    }
    
    return tx.Commit()
}

// Usage
err := WithTransaction(ctx, db, func(tx *sql.Tx) error {
    // All operations in this function are transactional
    if err := createOrder(tx, order); err != nil {
        return err
    }
    
    if err := updateInventory(tx, order.Items); err != nil {
        return err
    }
    
    if err := chargePayment(tx, order.Total); err != nil {
        return err
    }
    
    return nil
})
```

### Savepoints

```go
func ComplexTransaction(ctx context.Context, db *sql.DB) error {
    return WithTransaction(ctx, db, func(tx *sql.Tx) error {
        // Main transaction work
        _, err := tx.Exec("INSERT INTO orders (customer_id) VALUES ($1)", customerID)
        if err != nil {
            return err
        }
        
        // Create savepoint
        _, err = tx.Exec("SAVEPOINT process_items")
        if err != nil {
            return err
        }
        
        // Try to process items
        for _, item := range items {
            err := processItem(tx, item)
            if err != nil {
                // Rollback to savepoint
                tx.Exec("ROLLBACK TO SAVEPOINT process_items")
                
                // Handle error and maybe try alternative approach
                return handleItemError(tx, err)
            }
        }
        
        // Release savepoint if successful
        _, err = tx.Exec("RELEASE SAVEPOINT process_items")
        return err
    })
}
```

## Migrations

### Migration Files

```sql
-- migrations/001_create_users.up.sql
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_created_at ON users(created_at);

-- migrations/001_create_users.down.sql
DROP TABLE IF EXISTS users;
```

### Running Migrations

```go
import "github.com/golang-migrate/migrate/v4"

func RunMigrations(db *sql.DB) error {
    driver, err := postgres.WithInstance(db, &postgres.Config{})
    if err != nil {
        return err
    }
    
    m, err := migrate.NewWithDatabaseInstance(
        "file://migrations",
        "postgres", 
        driver,
    )
    if err != nil {
        return err
    }
    
    // Run all up migrations
    if err := m.Up(); err != nil && err != migrate.ErrNoChange {
        return err
    }
    
    return nil
}
```

## Best Practices

### 1. Use Prepared Statements

```go
// Prepare statement once
stmt, err := db.Prepare(`
    SELECT id, name, email 
    FROM users 
    WHERE status = $1 AND created_at > $2
`)
if err != nil {
    return err
}
defer stmt.Close()

// Reuse multiple times
for _, date := range dates {
    rows, err := stmt.Query("active", date)
    if err != nil {
        return err
    }
    
    // Process rows
    processRows(rows)
    rows.Close()
}
```

### 2. Handle NULL Values

```go
type User struct {
    ID          int
    Name        string
    Email       string
    PhoneNumber sql.NullString
    LastLoginAt sql.NullTime
}

// Scanning nullable fields
err := db.QueryRow("SELECT * FROM users WHERE id = $1", id).Scan(
    &user.ID,
    &user.Name,
    &user.Email,
    &user.PhoneNumber,
    &user.LastLoginAt,
)

// Using nullable fields
if user.PhoneNumber.Valid {
    fmt.Printf("Phone: %s\n", user.PhoneNumber.String)
}

if user.LastLoginAt.Valid {
    fmt.Printf("Last login: %s\n", user.LastLoginAt.Time)
}
```

### 3. Bulk Operations

```go
// Bulk insert using COPY
func BulkInsertUsers(users []User) error {
    txn, err := db.Begin()
    if err != nil {
        return err
    }
    
    stmt, err := txn.Prepare(pq.CopyIn("users", "name", "email", "created_at"))
    if err != nil {
        return err
    }
    
    for _, user := range users {
        _, err = stmt.Exec(user.Name, user.Email, user.CreatedAt)
        if err != nil {
            return err
        }
    }
    
    _, err = stmt.Exec()
    if err != nil {
        return err
    }
    
    err = stmt.Close()
    if err != nil {
        return err
    }
    
    return txn.Commit()
}
```

### 4. Connection Pooling

```go
// Configure connection pool
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(5)
db.SetConnMaxLifetime(5 * time.Minute)
db.SetConnMaxIdleTime(5 * time.Minute)

// Monitor pool health
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        stats := db.Stats()
        if stats.OpenConnections > 20 {
            log.Printf("Warning: High connection count: %d", stats.OpenConnections)
        }
        
        if stats.WaitCount > 0 {
            log.Printf("Warning: Connection pool wait count: %d", stats.WaitCount)
        }
    }
}()
```

### 5. Query Timeouts

```go
// Set query timeout using context
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

rows, err := db.QueryContext(ctx, "SELECT * FROM large_table")
if err != nil {
    if err == context.DeadlineExceeded {
        log.Println("Query timed out")
    }
    return err
}
```

### 6. Error Handling

```go
import "github.com/lib/pq"

func HandlePostgresError(err error) error {
    if err == nil {
        return nil
    }
    
    // Check for specific PostgreSQL errors
    if pqErr, ok := err.(*pq.Error); ok {
        switch pqErr.Code.Name() {
        case "unique_violation":
            return fmt.Errorf("duplicate key: %s", pqErr.Detail)
        case "foreign_key_violation":
            return fmt.Errorf("foreign key constraint: %s", pqErr.Detail)
        case "check_violation":
            return fmt.Errorf("check constraint: %s", pqErr.Detail)
        case "serialization_failure":
            return fmt.Errorf("transaction conflict, please retry")
        }
    }
    
    return err
}
```

### 7. Testing with PostgreSQL

```go
// Test helper for database setup
func SetupTestDB(t *testing.T) (*sql.DB, func()) {
    // Connect to test database
    db, err := sql.Open("postgres", "postgres://localhost/test_db?sslmode=disable")
    if err != nil {
        t.Fatal(err)
    }
    
    // Run migrations
    if err := RunMigrations(db); err != nil {
        t.Fatal(err)
    }
    
    // Return cleanup function
    cleanup := func() {
        // Clean all tables
        tables := []string{"users", "orders", "products"}
        for _, table := range tables {
            db.Exec(fmt.Sprintf("TRUNCATE %s CASCADE", table))
        }
        db.Close()
    }
    
    return db, cleanup
}

// Usage in tests
func TestUserRepository(t *testing.T) {
    db, cleanup := SetupTestDB(t)
    defer cleanup()
    
    repo := NewUserRepository(db)
    
    // Test create
    user, err := repo.Create("test@example.com", "Test User")
    assert.NoError(t, err)
    assert.NotZero(t, user.ID)
    
    // Test find
    found, err := repo.FindByID(user.ID)
    assert.NoError(t, err)
    assert.Equal(t, user.Email, found.Email)
}
```

### 8. JSON Operations

```go
// Working with JSONB columns
type Product struct {
    ID         int
    Name       string
    Attributes map[string]interface{}
}

// Insert JSON data
_, err := db.Exec(`
    INSERT INTO products (name, attributes) 
    VALUES ($1, $2)
`, product.Name, pq.Array(product.Attributes))

// Query JSON data
var attrs pq.StringArray
err := db.QueryRow(`
    SELECT attributes->>'color', attributes->>'size'
    FROM products 
    WHERE id = $1
`, id).Scan(&attrs)

// JSON aggregation
rows, err := db.Query(`
    SELECT 
        category,
        jsonb_agg(jsonb_build_object(
            'id', id,
            'name', name,
            'price', price
        )) as products
    FROM products
    GROUP BY category
`)
```

## Examples

### Complete Repository Example

```go
package repository

import (
    "context"
    "database/sql"
    "fmt"
    "time"
    
    "github.com/yourusername/commons-go/commons/postgres"
)

type User struct {
    ID        int       `db:"id"`
    Email     string    `db:"email"`
    Name      string    `db:"name"`
    CreatedAt time.Time `db:"created_at"`
    UpdatedAt time.Time `db:"updated_at"`
}

type UserRepository struct {
    db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
    return &UserRepository{db: db}
}

func (r *UserRepository) Create(ctx context.Context, user *User) error {
    query := `
        INSERT INTO users (email, name, created_at, updated_at)
        VALUES ($1, $2, $3, $4)
        RETURNING id
    `
    
    now := time.Now()
    err := r.db.QueryRowContext(
        ctx, query,
        user.Email, user.Name, now, now,
    ).Scan(&user.ID)
    
    if err != nil {
        return postgres.HandlePostgresError(err)
    }
    
    user.CreatedAt = now
    user.UpdatedAt = now
    return nil
}

func (r *UserRepository) FindByID(ctx context.Context, id int) (*User, error) {
    user := &User{}
    query := `
        SELECT id, email, name, created_at, updated_at
        FROM users
        WHERE id = $1
    `
    
    err := r.db.QueryRowContext(ctx, query, id).Scan(
        &user.ID,
        &user.Email,
        &user.Name,
        &user.CreatedAt,
        &user.UpdatedAt,
    )
    
    if err == sql.ErrNoRows {
        return nil, fmt.Errorf("user not found")
    }
    
    return user, err
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*User, error) {
    user := &User{}
    query := `
        SELECT id, email, name, created_at, updated_at
        FROM users
        WHERE email = $1
    `
    
    err := r.db.QueryRowContext(ctx, query, email).Scan(
        &user.ID,
        &user.Email,
        &user.Name,
        &user.CreatedAt,
        &user.UpdatedAt,
    )
    
    if err == sql.ErrNoRows {
        return nil, fmt.Errorf("user not found")
    }
    
    return user, err
}

func (r *UserRepository) Update(ctx context.Context, user *User) error {
    query := `
        UPDATE users
        SET email = $1, name = $2, updated_at = $3
        WHERE id = $4
    `
    
    user.UpdatedAt = time.Now()
    result, err := r.db.ExecContext(
        ctx, query,
        user.Email, user.Name, user.UpdatedAt, user.ID,
    )
    
    if err != nil {
        return postgres.HandlePostgresError(err)
    }
    
    rows, err := result.RowsAffected()
    if err != nil {
        return err
    }
    
    if rows == 0 {
        return fmt.Errorf("user not found")
    }
    
    return nil
}

func (r *UserRepository) Delete(ctx context.Context, id int) error {
    query := `DELETE FROM users WHERE id = $1`
    
    result, err := r.db.ExecContext(ctx, query, id)
    if err != nil {
        return err
    }
    
    rows, err := result.RowsAffected()
    if err != nil {
        return err
    }
    
    if rows == 0 {
        return fmt.Errorf("user not found")
    }
    
    return nil
}

func (r *UserRepository) List(ctx context.Context, page *postgres.Pagination) ([]*User, int64, error) {
    // Count total
    var total int64
    countQuery := `SELECT COUNT(*) FROM users`
    err := r.db.QueryRowContext(ctx, countQuery).Scan(&total)
    if err != nil {
        return nil, 0, err
    }
    
    // Get paginated results
    query := fmt.Sprintf(`
        SELECT id, email, name, created_at, updated_at
        FROM users
        ORDER BY %s
        LIMIT $1 OFFSET $2
    `, page.OrderBy)
    
    offset := (page.Page - 1) * page.Limit
    rows, err := r.db.QueryContext(ctx, query, page.Limit, offset)
    if err != nil {
        return nil, 0, err
    }
    defer rows.Close()
    
    var users []*User
    for rows.Next() {
        user := &User{}
        err := rows.Scan(
            &user.ID,
            &user.Email,
            &user.Name,
            &user.CreatedAt,
            &user.UpdatedAt,
        )
        if err != nil {
            return nil, 0, err
        }
        users = append(users, user)
    }
    
    return users, total, rows.Err()
}

func (r *UserRepository) Search(ctx context.Context, query string) ([]*User, error) {
    sql := `
        SELECT id, email, name, created_at, updated_at
        FROM users
        WHERE 
            email ILIKE $1 OR
            name ILIKE $1
        ORDER BY created_at DESC
        LIMIT 100
    `
    
    searchTerm := "%" + query + "%"
    rows, err := r.db.QueryContext(ctx, sql, searchTerm)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var users []*User
    for rows.Next() {
        user := &User{}
        err := rows.Scan(
            &user.ID,
            &user.Email,
            &user.Name,
            &user.CreatedAt,
            &user.UpdatedAt,
        )
        if err != nil {
            return nil, err
        }
        users = append(users, user)
    }
    
    return users, rows.Err()
}
```