# Plano de Implementação - Melhorias de Resiliência lib-commons

## Sumário Executivo

Este documento apresenta o plano de implementação para melhorias de resiliência nas conexões de banco de dados da lib-commons, abordando:
1. Thread-safety no Redis
2. Recuperação automática de connection pool após restart (PostgreSQL e MongoDB)

**Princípios do plano:**
- Manter 100% de backward compatibility
- Não alterar comportamento atual da lib
- Seguir padrões de código existentes
- Adicionar testes unitários para código novo
- Reforçar testes existentes quando necessário

---

## Categorização por Impacto

### HIGH IMPACT - Correções Críticas de Segurança/Estabilidade

| Item | Descrição | Arquivos Afetados |
|------|-----------|-------------------|
| H1 | Redis `GetClient()` thread-safety | `commons/redis/redis.go`, `commons/redis/redis_test.go` |

### MEDIUM IMPACT - Melhorias de Resiliência

| Item | Descrição | Arquivos Afetados |
|------|-----------|-------------------|
| M1 | PostgreSQL connection pool health check | `commons/postgres/postgres.go`, `commons/postgres/postgres_test.go` |
| M2 | MongoDB connection pool health check | `commons/mongo/mongo.go`, `commons/mongo/mongo_test.go` |

### LOW IMPACT - Melhorias Complementares

| Item | Descrição | Arquivos Afetados |
|------|-----------|-------------------|
| L1 | Redis connection health check | `commons/redis/redis.go`, `commons/redis/redis_test.go` |

---

## HIGH IMPACT

### H1: Redis GetClient() Thread-Safety

**Problema:**
`GetClient()` e `Connect()` não são thread-safe para inicialização concorrente. Múltiplas goroutines chamando `GetClient()` simultaneamente podem causar data races.

**Arquivo:** `commons/redis/redis.go`

**Código atual (problemático):**
```go
func (rc *RedisConnection) GetClient(ctx context.Context) (redis.UniversalClient, error) {
    if rc.Client == nil {        // Race: leitura sem lock
        if err := rc.Connect(ctx); err != nil {
            return nil, err
        }
    }
    return rc.Client, nil        // Race: leitura sem lock
}
```

**Solução proposta:**
Usar o mutex `mu` já existente na struct com padrão double-check locking:

```go
func (rc *RedisConnection) GetClient(ctx context.Context) (redis.UniversalClient, error) {
    rc.mu.RLock()
    client := rc.Client
    rc.mu.RUnlock()

    if client != nil {
        return client, nil
    }

    rc.mu.Lock()
    defer rc.mu.Unlock()

    if rc.Client != nil {
        return rc.Client, nil
    }

    if err := rc.Connect(ctx); err != nil {
        return nil, err
    }

    return rc.Client, nil
}
```

**Alterações no `Connect()`:**
O método `Connect()` já é chamado dentro do lock em `GetClient()`, mas deve proteger a escrita em `rc.Client`:

```go
func (rc *RedisConnection) Connect(ctx context.Context) error {
    // ... código existente de criação do client ...

    // Antes de atribuir:
    // rc.Client = client (já está dentro do lock de GetClient)

    // ... resto do código ...
}
```

**Backward Compatibility:**
- A assinatura dos métodos não muda
- Comportamento externo idêntico
- Apenas adiciona sincronização interna

**Testes a adicionar em `commons/redis/redis_test.go`:**

```go
func TestGetClient_ConcurrentAccess(t *testing.T) {
    // Teste com múltiplas goroutines chamando GetClient simultaneamente
    // Verificar que apenas uma conexão é criada
    // Usar WaitGroup e -race flag
}

func TestGetClient_ConcurrentAccess_WithRaceDetector(t *testing.T) {
    // Teste específico para validar ausência de data races
    // Executar com: go test -race
}
```

**Checklist de implementação:**
- [ ] Modificar `GetClient()` com double-check locking
- [ ] Garantir que `Connect()` protege escrita em `rc.Client`
- [ ] Adicionar teste de acesso concorrente
- [ ] Executar testes com `-race` flag
- [ ] Validar que testes existentes continuam passando

---

## MEDIUM IMPACT

### M1: PostgreSQL Connection Pool Health Check

**Problema:**
O connection pool não recupera automaticamente após restart do container PostgreSQL. Conexões stale permanecem no pool.

**Arquivo:** `commons/postgres/postgres.go`

**Abordagem:**
Configurar parâmetros de health check do driver pgx/dbresolver que já existem mas podem não estar otimizados.

**Configurações a adicionar/ajustar:**

```go
// Na função Connect(), adicionar configuração de health check
func (pc *PostgresConnection) Connect() error {
    // ... código existente ...

    // Adicionar opções de health check ao pool
    // Estas configurações habilitam ping periódico e descarte de conexões stale

    // ConnMaxLifetime já existe (30 min), manter
    // ConnMaxIdleTime: tempo máximo que conexão fica idle antes de ser fechada
    // Adicionar se não existir:
    connPool.SetConnMaxIdleTime(5 * time.Minute)

    // ... resto do código ...
}
```

**Nova função opcional de Health Check:**

```go
// Ping verifica a saúde da conexão com o banco de dados
// Pode ser usado por health checkers externos
func (pc *PostgresConnection) Ping(ctx context.Context) error {
    if pc.ConnectionDB == nil {
        return ErrNotConnected
    }
    return pc.ConnectionDB.PingContext(ctx)
}
```

**Backward Compatibility:**
- `Ping()` é método novo opcional, não afeta código existente
- Configurações de pool são internas ao driver
- Nenhuma mudança de interface pública

**Testes a adicionar em `commons/postgres/postgres_test.go`:**

```go
func TestPing_Connected(t *testing.T) {
    // Testa Ping em conexão ativa
}

func TestPing_NotConnected(t *testing.T) {
    // Testa Ping retorna erro quando não conectado
}

func TestConnectionPool_IdleTimeout(t *testing.T) {
    // Testa que conexões idle são fechadas após timeout
}
```

**Checklist de implementação:**
- [ ] Adicionar constante `ErrNotConnected`
- [ ] Implementar método `Ping(ctx context.Context) error`
- [ ] Configurar `ConnMaxIdleTime` se não existir
- [ ] Adicionar testes unitários para `Ping()`
- [ ] Validar testes existentes continuam passando

---

### M2: MongoDB Connection Pool Health Check

**Problema:**
Similar ao PostgreSQL - connection pool não recupera após restart do container MongoDB.

**Arquivo:** `commons/mongo/mongo.go`

**Abordagem:**
Expor método de health check e garantir configurações adequadas do driver.

**Nova função de Health Check:**

```go
// Ping verifica a saúde da conexão com o MongoDB
// Pode ser usado por health checkers externos
func (mc *MongoConnection) Ping(ctx context.Context) error {
    if mc.DB == nil {
        return ErrNotConnected
    }
    return mc.DB.Ping(ctx, readpref.Primary())
}
```

**Configurações de pool a verificar/adicionar no Connect():**

```go
func (mc *MongoConnection) Connect() error {
    // ... código existente ...

    // Garantir que ServerSelectionTimeout está configurado adequadamente
    // para falhar rápido em conexões stale
    clientOptions := options.Client().
        ApplyURI(mc.ConnectionStringSource).
        SetMaxPoolSize(mc.MaxPoolSize).
        SetServerSelectionTimeout(5 * time.Second). // Fail fast
        SetHeartbeatInterval(10 * time.Second)      // Detectar nós mortos

    // ... resto do código ...
}
```

**Backward Compatibility:**
- `Ping()` é método novo opcional
- Configurações de timeout são internas ao driver
- Clientes existentes não são afetados

**Testes a adicionar em `commons/mongo/mongo_test.go`:**

```go
func TestPing_Connected(t *testing.T) {
    // Testa Ping em conexão ativa
}

func TestPing_NotConnected(t *testing.T) {
    // Testa Ping retorna erro quando não conectado
}
```

**Checklist de implementação:**
- [ ] Adicionar constante `ErrNotConnected`
- [ ] Implementar método `Ping(ctx context.Context) error`
- [ ] Verificar/ajustar `ServerSelectionTimeout`
- [ ] Verificar/ajustar `HeartbeatInterval`
- [ ] Adicionar testes unitários para `Ping()`
- [ ] Validar testes existentes continuam passando

---

## LOW IMPACT

### L1: Redis Connection Health Check

**Problema:**
Redis não tem método `Ping()` exposto para health checks externos, embora use ping internamente.

**Arquivo:** `commons/redis/redis.go`

**Solução:**
Expor método Ping consistente com PostgreSQL e MongoDB:

```go
// Ping verifica a saúde da conexão com o Redis
// Pode ser usado por health checkers externos
func (rc *RedisConnection) Ping(ctx context.Context) error {
    rc.mu.RLock()
    client := rc.Client
    rc.mu.RUnlock()

    if client == nil {
        return ErrNotConnected
    }

    return client.Ping(ctx).Err()
}
```

**Backward Compatibility:**
- Método novo opcional
- Não afeta código existente

**Testes a adicionar em `commons/redis/redis_test.go`:**

```go
func TestPing_Connected(t *testing.T) {
    // Testa Ping em conexão ativa usando miniredis
}

func TestPing_NotConnected(t *testing.T) {
    // Testa Ping retorna erro quando Client é nil
}
```

**Checklist de implementação:**
- [ ] Adicionar constante `ErrNotConnected` se não existir
- [ ] Implementar método `Ping(ctx context.Context) error`
- [ ] Adicionar testes unitários
- [ ] Validar testes existentes continuam passando

---

## Ordem de Implementação Recomendada

1. **H1 - Redis Thread-Safety** (Prioridade máxima - bug de segurança)
2. **M1 - PostgreSQL Health Check**
3. **M2 - MongoDB Health Check**
4. **L1 - Redis Health Check**

---

## Padrões de Código a Seguir

### Definição de Erros
Seguir padrão existente na lib:
```go
var (
    ErrNotConnected = errors.New("connection not established")
)
```

### Padrão de Testes
Seguir padrão table-driven existente:
```go
func TestXxx(t *testing.T) {
    tests := []struct {
        name        string
        setup       func() *Connection
        expectError bool
    }{
        {
            name: "success case",
            setup: func() *Connection { /* ... */ },
            expectError: false,
        },
        // ...
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // ...
        })
    }
}
```

### Uso de Mutex
Seguir padrão double-check locking já usado no pool-manager:
```go
func (x *Struct) GetResource() Resource {
    x.mu.RLock()
    res := x.resource
    x.mu.RUnlock()

    if res != nil {
        return res
    }

    x.mu.Lock()
    defer x.mu.Unlock()

    if x.resource != nil {
        return x.resource
    }

    // criar resource
    return x.resource
}
```

---

## Validação de Backward Compatibility

### Checklist Geral
- [ ] Nenhuma assinatura de método público foi alterada
- [ ] Nenhum método público foi removido
- [ ] Novos métodos são opcionais (não quebram código existente)
- [ ] Configurações internas não afetam comportamento externo
- [ ] Todos os testes existentes passam
- [ ] Código compila sem erros

### Testes de Regressão
Executar suite completa de testes:
```bash
go test ./... -v
go test ./... -race
```

---

## Estimativa de Arquivos Modificados

| Arquivo | Tipo de Alteração |
|---------|-------------------|
| `commons/redis/redis.go` | Modificação (thread-safety + Ping) |
| `commons/redis/redis_test.go` | Adição de testes |
| `commons/postgres/postgres.go` | Adição de método Ping |
| `commons/postgres/postgres_test.go` | Adição de testes |
| `commons/mongo/mongo.go` | Adição de método Ping |
| `commons/mongo/mongo_test.go` | Adição de testes |

**Total:** 6 arquivos

---

## Critérios de Aceitação

1. Todos os testes passam (incluindo novos)
2. `go test -race` não reporta data races
3. Código segue padrões existentes do projeto
4. Backward compatibility mantida (sem breaking changes)
5. Cobertura de testes adequada para código novo
