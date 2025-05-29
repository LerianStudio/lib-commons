# MongoDB Integration

The MongoDB package provides connection management, query helpers, and utilities for working with MongoDB databases.

## Table of Contents

- [Overview](#overview)
- [Connection Management](#connection-management)
- [CRUD Operations](#crud-operations)
- [Query Building](#query-building)
- [Aggregation Pipeline](#aggregation-pipeline)
- [Transactions](#transactions)
- [Indexes](#indexes)
- [Best Practices](#best-practices)

## Overview

The MongoDB integration provides:

- Connection pooling and management
- Context-aware operations
- Transaction support
- Aggregation pipeline helpers
- Index management
- Change streams support
- GridFS for file storage

## Connection Management

### Basic Connection

```go
import (
    "github.com/yourusername/commons-go/commons/mongo"
)

// Connect using environment variables
conn, err := mongo.Connect(context.Background())
if err != nil {
    log.Fatal(err)
}
defer conn.Close(context.Background())

// Get database and collection
db := conn.Database("myapp")
collection := db.Collection("users")
```

### Connection with Configuration

```go
config := mongo.Config{
    URI:            "mongodb://localhost:27017",
    Database:       "myapp",
    MinPoolSize:    5,
    MaxPoolSize:    100,
    MaxIdleTime:    5 * time.Minute,
    ConnectTimeout: 10 * time.Second,
}

conn, err := mongo.ConnectWithConfig(context.Background(), config)
if err != nil {
    log.Fatal(err)
}
```

### Connection Options

```go
// With authentication
config := mongo.Config{
    URI:      "mongodb://username:password@localhost:27017",
    Database: "myapp",
}

// With replica set
config := mongo.Config{
    URI:      "mongodb://host1:27017,host2:27017,host3:27017/?replicaSet=myrs",
    Database: "myapp",
}

// With TLS
config := mongo.Config{
    URI:      "mongodb://localhost:27017/?ssl=true",
    Database: "myapp",
    TLSConfig: &tls.Config{
        // TLS configuration
    },
}
```

## CRUD Operations

### Insert Operations

```go
// Insert one document
user := User{
    Name:      "John Doe",
    Email:     "john@example.com",
    CreatedAt: time.Now(),
}

result, err := collection.InsertOne(ctx, user)
if err != nil {
    return err
}

insertedID := result.InsertedID.(primitive.ObjectID)
log.Printf("Inserted user with ID: %s", insertedID.Hex())

// Insert many documents
users := []interface{}{
    User{Name: "Alice", Email: "alice@example.com"},
    User{Name: "Bob", Email: "bob@example.com"},
}

results, err := collection.InsertMany(ctx, users)
if err != nil {
    return err
}

log.Printf("Inserted %d users", len(results.InsertedIDs))
```

### Find Operations

```go
// Find one document
var user User
err := collection.FindOne(ctx, bson.M{"email": "john@example.com"}).Decode(&user)
if err != nil {
    if err == mongo.ErrNoDocuments {
        log.Println("User not found")
    }
    return err
}

// Find many documents
cursor, err := collection.Find(ctx, bson.M{"status": "active"})
if err != nil {
    return err
}
defer cursor.Close(ctx)

var users []User
if err := cursor.All(ctx, &users); err != nil {
    return err
}

// Find with options
opts := options.Find().
    SetSort(bson.D{{"created_at", -1}}).
    SetLimit(10).
    SetSkip(20)

cursor, err := collection.Find(ctx, bson.M{}, opts)
```

### Update Operations

```go
// Update one document
filter := bson.M{"_id": userID}
update := bson.M{
    "$set": bson.M{
        "name":       "Jane Doe",
        "updated_at": time.Now(),
    },
}

result, err := collection.UpdateOne(ctx, filter, update)
if err != nil {
    return err
}

log.Printf("Matched %d, Modified %d", result.MatchedCount, result.ModifiedCount)

// Update many documents
filter = bson.M{"status": "pending"}
update = bson.M{
    "$set": bson.M{"status": "processing"},
    "$inc": bson.M{"retry_count": 1},
}

result, err = collection.UpdateMany(ctx, filter, update)

// Upsert
opts := options.Update().SetUpsert(true)
result, err = collection.UpdateOne(ctx, filter, update, opts)
```

### Delete Operations

```go
// Delete one document
result, err := collection.DeleteOne(ctx, bson.M{"_id": userID})
if err != nil {
    return err
}

log.Printf("Deleted %d document", result.DeletedCount)

// Delete many documents
filter := bson.M{
    "created_at": bson.M{
        "$lt": time.Now().Add(-30 * 24 * time.Hour), // 30 days ago
    },
}

result, err = collection.DeleteMany(ctx, filter)
log.Printf("Deleted %d documents", result.DeletedCount)
```

## Query Building

### Complex Queries

```go
// Query builder helper
type QueryBuilder struct {
    filter bson.M
}

func NewQueryBuilder() *QueryBuilder {
    return &QueryBuilder{filter: bson.M{}}
}

func (q *QueryBuilder) Where(field string, value interface{}) *QueryBuilder {
    q.filter[field] = value
    return q
}

func (q *QueryBuilder) WhereIn(field string, values []interface{}) *QueryBuilder {
    q.filter[field] = bson.M{"$in": values}
    return q
}

func (q *QueryBuilder) WhereBetween(field string, min, max interface{}) *QueryBuilder {
    q.filter[field] = bson.M{"$gte": min, "$lte": max}
    return q
}

func (q *QueryBuilder) Build() bson.M {
    return q.filter
}

// Usage
query := NewQueryBuilder().
    Where("status", "active").
    WhereIn("role", []interface{}{"admin", "moderator"}).
    WhereBetween("age", 18, 65).
    Build()

cursor, err := collection.Find(ctx, query)
```

### Text Search

```go
// Create text index
indexModel := mongo.IndexModel{
    Keys: bson.D{
        {"title", "text"},
        {"content", "text"},
    },
}
_, err := collection.Indexes().CreateOne(ctx, indexModel)

// Perform text search
filter := bson.M{
    "$text": bson.M{
        "$search": "mongodb tutorial",
    },
}

opts := options.Find().SetProjection(bson.M{
    "score": bson.M{"$meta": "textScore"},
}).SetSort(bson.M{
    "score": bson.M{"$meta": "textScore"},
})

cursor, err := collection.Find(ctx, filter, opts)
```

## Aggregation Pipeline

### Basic Aggregation

```go
// Simple aggregation pipeline
pipeline := mongo.Pipeline{
    {{"$match", bson.D{{"status", "completed"}}}},
    {{"$group", bson.D{
        {"_id", "$customer_id"},
        {"total", bson.D{{"$sum", "$amount"}}},
        {"count", bson.D{{"$sum", 1}}},
    }}},
    {{"$sort", bson.D{{"total", -1}}}},
    {{"$limit", 10}},
}

cursor, err := collection.Aggregate(ctx, pipeline)
if err != nil {
    return err
}
defer cursor.Close(ctx)

var results []bson.M
if err := cursor.All(ctx, &results); err != nil {
    return err
}
```

### Complex Aggregation

```go
// Sales report aggregation
pipeline := mongo.Pipeline{
    // Match orders from last month
    {{"$match", bson.D{
        {"created_at", bson.D{
            {"$gte", startOfMonth},
            {"$lt", endOfMonth},
        }},
    }}},
    
    // Lookup customer details
    {{"$lookup", bson.D{
        {"from", "customers"},
        {"localField", "customer_id"},
        {"foreignField", "_id"},
        {"as", "customer"},
    }}},
    
    // Unwind customer array
    {{"$unwind", "$customer"}},
    
    // Group by product category
    {{"$group", bson.D{
        {"_id", "$product.category"},
        {"revenue", bson.D{{"$sum", "$amount"}}},
        {"orders", bson.D{{"$sum", 1}}},
        {"customers", bson.D{{"$addToSet", "$customer._id"}}},
    }}},
    
    // Calculate customer count
    {{"$addFields", bson.D{
        {"customer_count", bson.D{{"$size", "$customers"}}},
    }}},
    
    // Project final fields
    {{"$project", bson.D{
        {"category", "$_id"},
        {"revenue", 1},
        {"orders", 1},
        {"customer_count", 1},
        {"_id", 0},
    }}},
    
    // Sort by revenue
    {{"$sort", bson.D{{"revenue", -1}}}},
}

type CategoryReport struct {
    Category      string  `bson:"category"`
    Revenue       float64 `bson:"revenue"`
    Orders        int     `bson:"orders"`
    CustomerCount int     `bson:"customer_count"`
}

var reports []CategoryReport
cursor, err := collection.Aggregate(ctx, pipeline)
if err != nil {
    return err
}

if err := cursor.All(ctx, &reports); err != nil {
    return err
}
```

### Faceted Search

```go
// Multi-facet aggregation
pipeline := mongo.Pipeline{
    // Initial match
    {{"$match", bson.D{{"status", "active"}}}},
    
    // Faceted aggregation
    {{"$facet", bson.D{
        // Price ranges
        {"price_ranges", bson.A{
            bson.D{{"$bucket", bson.D{
                {"groupBy", "$price"},
                {"boundaries", bson.A{0, 10, 50, 100, 500}},
                {"default", "Other"},
                {"output", bson.D{
                    {"count", bson.D{{"$sum", 1}}},
                    {"products", bson.D{{"$push", "$name"}}},
                }},
            }}},
        }},
        
        // Categories
        {"categories", bson.A{
            bson.D{{"$group", bson.D{
                {"_id", "$category"},
                {"count", bson.D{{"$sum", 1}}},
            }}},
            bson.D{{"$sort", bson.D{{"count", -1}}}},
        }},
        
        // Top rated
        {"top_rated", bson.A{
            bson.D{{"$sort", bson.D{{"rating", -1}}}},
            bson.D{{"$limit", 5}},
            bson.D{{"$project", bson.D{
                {"name", 1},
                {"rating", 1},
            }}},
        }},
    }}},
}
```

## Transactions

### Basic Transactions

```go
// Start a session
session, err := client.StartSession()
if err != nil {
    return err
}
defer session.EndSession(ctx)

// Execute transaction
err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
    // Start transaction
    if err := session.StartTransaction(); err != nil {
        return err
    }
    
    // Perform operations
    _, err := accountsColl.UpdateOne(sc, 
        bson.M{"_id": fromAccount},
        bson.M{"$inc": bson.M{"balance": -amount}},
    )
    if err != nil {
        return err
    }
    
    _, err = accountsColl.UpdateOne(sc,
        bson.M{"_id": toAccount},
        bson.M{"$inc": bson.M{"balance": amount}},
    )
    if err != nil {
        return err
    }
    
    // Insert transfer record
    _, err = transfersColl.InsertOne(sc, bson.M{
        "from":      fromAccount,
        "to":        toAccount,
        "amount":    amount,
        "timestamp": time.Now(),
    })
    if err != nil {
        return err
    }
    
    // Commit transaction
    return session.CommitTransaction(sc)
})

if err != nil {
    // Transaction will be aborted automatically
    return err
}
```

### Transaction with Retry

```go
func ExecuteWithRetry(ctx context.Context, fn func(mongo.SessionContext) error) error {
    session, err := client.StartSession()
    if err != nil {
        return err
    }
    defer session.EndSession(ctx)
    
    maxRetries := 3
    for i := 0; i < maxRetries; i++ {
        err = mongo.WithSession(ctx, session, func(sc mongo.SessionContext) error {
            if err := session.StartTransaction(); err != nil {
                return err
            }
            
            err := fn(sc)
            if err != nil {
                return err
            }
            
            return session.CommitTransaction(sc)
        })
        
        if err == nil {
            return nil
        }
        
        // Check if retryable
        if !mongo.IsTimeout(err) && !mongo.IsNetworkError(err) {
            return err
        }
        
        log.Printf("Transaction failed, retrying... (%d/%d)", i+1, maxRetries)
        time.Sleep(time.Second * time.Duration(i+1))
    }
    
    return err
}
```

## Indexes

### Creating Indexes

```go
// Single field index
indexModel := mongo.IndexModel{
    Keys: bson.D{{"email", 1}},
    Options: options.Index().
        SetUnique(true).
        SetBackground(true),
}

indexName, err := collection.Indexes().CreateOne(ctx, indexModel)

// Compound index
indexModel = mongo.IndexModel{
    Keys: bson.D{
        {"user_id", 1},
        {"created_at", -1},
    },
}

// TTL index
indexModel = mongo.IndexModel{
    Keys: bson.D{{"expires_at", 1}},
    Options: options.Index().
        SetExpireAfterSeconds(0),
}

// Partial index
indexModel = mongo.IndexModel{
    Keys: bson.D{{"email", 1}},
    Options: options.Index().
        SetPartialFilterExpression(bson.M{
            "status": "active",
        }),
}
```

### Managing Indexes

```go
// List all indexes
cursor, err := collection.Indexes().List(ctx)
if err != nil {
    return err
}

var indexes []bson.M
if err := cursor.All(ctx, &indexes); err != nil {
    return err
}

for _, index := range indexes {
    log.Printf("Index: %v", index)
}

// Drop index
_, err = collection.Indexes().DropOne(ctx, "email_1")

// Drop all indexes except _id
_, err = collection.Indexes().DropAll(ctx)
```

## Best Practices

### 1. Use Proper Data Types

```go
type User struct {
    ID        primitive.ObjectID `bson:"_id,omitempty"`
    Name      string             `bson:"name"`
    Email     string             `bson:"email"`
    Age       int32              `bson:"age"`
    Balance   primitive.Decimal128 `bson:"balance"`
    Tags      []string           `bson:"tags"`
    CreatedAt time.Time          `bson:"created_at"`
    UpdatedAt time.Time          `bson:"updated_at"`
}

// Working with ObjectID
userID, err := primitive.ObjectIDFromHex("507f1f77bcf86cd799439011")
if err != nil {
    return err
}

// Working with Decimal128
balance, err := primitive.ParseDecimal128("123.45")
if err != nil {
    return err
}
```

### 2. Connection Pooling

```go
// Monitor connection pool
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        // Get server status
        var result bson.M
        err := client.Database("admin").RunCommand(
            context.Background(),
            bson.D{{"serverStatus", 1}},
        ).Decode(&result)
        
        if err != nil {
            log.Printf("Failed to get server status: %v", err)
            continue
        }
        
        connections := result["connections"].(bson.M)
        log.Printf("Current connections: %v", connections["current"])
        log.Printf("Available connections: %v", connections["available"])
    }
}()
```

### 3. Error Handling

```go
func HandleMongoError(err error) error {
    if err == nil {
        return nil
    }
    
    // Check for duplicate key error
    if mongo.IsDuplicateKeyError(err) {
        return fmt.Errorf("duplicate key error: %w", err)
    }
    
    // Check for timeout
    if mongo.IsTimeout(err) {
        return fmt.Errorf("operation timed out: %w", err)
    }
    
    // Check for network error
    if mongo.IsNetworkError(err) {
        return fmt.Errorf("network error: %w", err)
    }
    
    // Check for write errors
    var writeErr mongo.WriteException
    if errors.As(err, &writeErr) {
        for _, we := range writeErr.WriteErrors {
            if we.Code == 11000 {
                return fmt.Errorf("duplicate key error")
            }
        }
    }
    
    return err
}
```

### 4. Bulk Operations

```go
// Bulk write operations
models := []mongo.WriteModel{
    mongo.NewInsertOneModel().SetDocument(bson.M{
        "name": "Alice",
        "email": "alice@example.com",
    }),
    mongo.NewUpdateOneModel().
        SetFilter(bson.M{"email": "bob@example.com"}).
        SetUpdate(bson.M{"$set": bson.M{"name": "Robert"}}),
    mongo.NewDeleteOneModel().
        SetFilter(bson.M{"status": "inactive"}),
}

opts := options.BulkWrite().SetOrdered(false)
result, err := collection.BulkWrite(ctx, models, opts)
if err != nil {
    return err
}

log.Printf("Inserted: %d, Updated: %d, Deleted: %d",
    result.InsertedCount,
    result.ModifiedCount,
    result.DeletedCount)
```

### 5. Change Streams

```go
// Watch for changes
pipeline := mongo.Pipeline{
    {{"$match", bson.D{
        {"operationType", bson.D{{"$in", bson.A{"insert", "update"}}}},
    }}},
}

opts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
stream, err := collection.Watch(ctx, pipeline, opts)
if err != nil {
    return err
}
defer stream.Close(ctx)

// Process changes
for stream.Next(ctx) {
    var changeEvent bson.M
    if err := stream.Decode(&changeEvent); err != nil {
        log.Printf("Error decoding change event: %v", err)
        continue
    }
    
    log.Printf("Change event: %v", changeEvent)
    
    // Handle specific operations
    switch changeEvent["operationType"] {
    case "insert":
        handleInsert(changeEvent["fullDocument"])
    case "update":
        handleUpdate(changeEvent["documentKey"], changeEvent["updateDescription"])
    }
}
```

### 6. GridFS for File Storage

```go
// Upload file to GridFS
bucket, err := gridfs.NewBucket(
    database,
    options.GridFSBucket().SetName("files"),
)
if err != nil {
    return err
}

uploadStream, err := bucket.OpenUploadStream(
    "myfile.pdf",
    options.GridFSUpload().
        SetMetadata(bson.M{
            "user_id": userID,
            "type": "document",
        }),
)
if err != nil {
    return err
}
defer uploadStream.Close()

// Write file data
fileData, err := ioutil.ReadFile("myfile.pdf")
if err != nil {
    return err
}

_, err = uploadStream.Write(fileData)
if err != nil {
    return err
}

fileID := uploadStream.FileID

// Download file from GridFS
downloadStream, err := bucket.OpenDownloadStream(fileID)
if err != nil {
    return err
}
defer downloadStream.Close()

// Read file data
fileBytes, err := ioutil.ReadAll(downloadStream)
if err != nil {
    return err
}
```

### 7. Testing with MongoDB

```go
// Test container setup
func SetupTestMongo(t *testing.T) (*mongo.Client, func()) {
    ctx := context.Background()
    
    // Start MongoDB container
    req := testcontainers.ContainerRequest{
        Image:        "mongo:5.0",
        ExposedPorts: []string{"27017/tcp"},
        WaitingFor:   wait.ForListeningPort("27017/tcp"),
    }
    
    mongoC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: req,
        Started:          true,
    })
    if err != nil {
        t.Fatal(err)
    }
    
    // Get connection string
    host, err := mongoC.Host(ctx)
    if err != nil {
        t.Fatal(err)
    }
    
    port, err := mongoC.MappedPort(ctx, "27017")
    if err != nil {
        t.Fatal(err)
    }
    
    uri := fmt.Sprintf("mongodb://%s:%s", host, port.Port())
    
    // Connect to MongoDB
    client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
    if err != nil {
        t.Fatal(err)
    }
    
    // Cleanup function
    cleanup := func() {
        client.Disconnect(ctx)
        mongoC.Terminate(ctx)
    }
    
    return client, cleanup
}

// Usage in tests
func TestUserRepository(t *testing.T) {
    client, cleanup := SetupTestMongo(t)
    defer cleanup()
    
    db := client.Database("test")
    repo := NewUserRepository(db)
    
    // Run tests
    t.Run("CreateUser", func(t *testing.T) {
        user := &User{
            Name:  "Test User",
            Email: "test@example.com",
        }
        
        err := repo.Create(context.Background(), user)
        assert.NoError(t, err)
        assert.NotZero(t, user.ID)
    })
}
```

## Examples

### Complete Repository Example

```go
package repository

import (
    "context"
    "fmt"
    "time"
    
    "go.mongodb.org/mongo-driver/bson"
    "go.mongodb.org/mongo-driver/bson/primitive"
    "go.mongodb.org/mongo-driver/mongo"
    "go.mongodb.org/mongo-driver/mongo/options"
)

type User struct {
    ID        primitive.ObjectID `bson:"_id,omitempty"`
    Email     string             `bson:"email"`
    Name      string             `bson:"name"`
    Role      string             `bson:"role"`
    Status    string             `bson:"status"`
    Metadata  map[string]interface{} `bson:"metadata,omitempty"`
    CreatedAt time.Time          `bson:"created_at"`
    UpdatedAt time.Time          `bson:"updated_at"`
}

type UserRepository struct {
    collection *mongo.Collection
}

func NewUserRepository(db *mongo.Database) *UserRepository {
    collection := db.Collection("users")
    
    // Create indexes
    indexes := []mongo.IndexModel{
        {
            Keys:    bson.D{{"email", 1}},
            Options: options.Index().SetUnique(true),
        },
        {
            Keys: bson.D{{"status", 1}, {"created_at", -1}},
        },
        {
            Keys: bson.D{{"name", "text"}},
        },
    }
    
    _, err := collection.Indexes().CreateMany(context.Background(), indexes)
    if err != nil {
        log.Printf("Failed to create indexes: %v", err)
    }
    
    return &UserRepository{
        collection: collection,
    }
}

func (r *UserRepository) Create(ctx context.Context, user *User) error {
    user.ID = primitive.NewObjectID()
    user.CreatedAt = time.Now()
    user.UpdatedAt = user.CreatedAt
    user.Status = "active"
    
    _, err := r.collection.InsertOne(ctx, user)
    return err
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (*User, error) {
    objectID, err := primitive.ObjectIDFromHex(id)
    if err != nil {
        return nil, fmt.Errorf("invalid ID format")
    }
    
    var user User
    err = r.collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&user)
    if err == mongo.ErrNoDocuments {
        return nil, fmt.Errorf("user not found")
    }
    
    return &user, err
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*User, error) {
    var user User
    err := r.collection.FindOne(ctx, bson.M{"email": email}).Decode(&user)
    if err == mongo.ErrNoDocuments {
        return nil, fmt.Errorf("user not found")
    }
    
    return &user, err
}

func (r *UserRepository) Update(ctx context.Context, id string, updates bson.M) error {
    objectID, err := primitive.ObjectIDFromHex(id)
    if err != nil {
        return fmt.Errorf("invalid ID format")
    }
    
    updates["updated_at"] = time.Now()
    
    result, err := r.collection.UpdateOne(
        ctx,
        bson.M{"_id": objectID},
        bson.M{"$set": updates},
    )
    
    if err != nil {
        return err
    }
    
    if result.MatchedCount == 0 {
        return fmt.Errorf("user not found")
    }
    
    return nil
}

func (r *UserRepository) Delete(ctx context.Context, id string) error {
    objectID, err := primitive.ObjectIDFromHex(id)
    if err != nil {
        return fmt.Errorf("invalid ID format")
    }
    
    result, err := r.collection.DeleteOne(ctx, bson.M{"_id": objectID})
    if err != nil {
        return err
    }
    
    if result.DeletedCount == 0 {
        return fmt.Errorf("user not found")
    }
    
    return nil
}

func (r *UserRepository) List(ctx context.Context, filter bson.M, page, limit int) ([]*User, int64, error) {
    // Count total
    total, err := r.collection.CountDocuments(ctx, filter)
    if err != nil {
        return nil, 0, err
    }
    
    // Find with pagination
    skip := (page - 1) * limit
    opts := options.Find().
        SetSort(bson.D{{"created_at", -1}}).
        SetSkip(int64(skip)).
        SetLimit(int64(limit))
    
    cursor, err := r.collection.Find(ctx, filter, opts)
    if err != nil {
        return nil, 0, err
    }
    defer cursor.Close(ctx)
    
    var users []*User
    if err := cursor.All(ctx, &users); err != nil {
        return nil, 0, err
    }
    
    return users, total, nil
}

func (r *UserRepository) Search(ctx context.Context, query string) ([]*User, error) {
    filter := bson.M{
        "$text": bson.M{"$search": query},
    }
    
    opts := options.Find().
        SetProjection(bson.M{
            "score": bson.M{"$meta": "textScore"},
        }).
        SetSort(bson.M{
            "score": bson.M{"$meta": "textScore"},
        }).
        SetLimit(100)
    
    cursor, err := r.collection.Find(ctx, filter, opts)
    if err != nil {
        return nil, err
    }
    defer cursor.Close(ctx)
    
    var users []*User
    if err := cursor.All(ctx, &users); err != nil {
        return nil, err
    }
    
    return users, nil
}

func (r *UserRepository) UpdateStatus(ctx context.Context, userIDs []string, status string) error {
    objectIDs := make([]primitive.ObjectID, len(userIDs))
    for i, id := range userIDs {
        oid, err := primitive.ObjectIDFromHex(id)
        if err != nil {
            return fmt.Errorf("invalid ID format: %s", id)
        }
        objectIDs[i] = oid
    }
    
    filter := bson.M{
        "_id": bson.M{"$in": objectIDs},
    }
    
    update := bson.M{
        "$set": bson.M{
            "status":     status,
            "updated_at": time.Now(),
        },
    }
    
    _, err := r.collection.UpdateMany(ctx, filter, update)
    return err
}

func (r *UserRepository) GetStatsByRole(ctx context.Context) ([]RoleStats, error) {
    pipeline := mongo.Pipeline{
        {{"$match", bson.D{{"status", "active"}}}},
        {{"$group", bson.D{
            {"_id", "$role"},
            {"count", bson.D{{"$sum", 1}}},
            {"last_created", bson.D{{"$max", "$created_at"}}},
        }}},
        {{"$project", bson.D{
            {"role", "$_id"},
            {"count", 1},
            {"last_created", 1},
            {"_id", 0},
        }}},
        {{"$sort", bson.D{{"count", -1}}}},
    }
    
    cursor, err := r.collection.Aggregate(ctx, pipeline)
    if err != nil {
        return nil, err
    }
    defer cursor.Close(ctx)
    
    var stats []RoleStats
    if err := cursor.All(ctx, &stats); err != nil {
        return nil, err
    }
    
    return stats, nil
}

type RoleStats struct {
    Role        string    `bson:"role"`
    Count       int       `bson:"count"`
    LastCreated time.Time `bson:"last_created"`
}
```