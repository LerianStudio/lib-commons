# Prompt de Execução - Melhorias de Resiliência lib-commons

## Contexto

Execute o plano de implementação descrito em `IMPLEMENTATION_PLAN.md` para melhorias de resiliência nas conexões de banco de dados da lib-commons.

## Instruções

Implemente as melhorias na seguinte ordem de prioridade:

### 1. H1 - Redis Thread-Safety (HIGH IMPACT)

**Arquivo:** `commons/redis/redis.go`

- Modificar `GetClient()` para usar double-check locking com o mutex `mu` já existente na struct
- Garantir que `Connect()` protege a escrita em `rc.Client`
- Adicionar testes de acesso concorrente em `commons/redis/redis_test.go`
- Executar testes com `-race` flag para validar ausência de data races

### 2. M1 - PostgreSQL Connection Pool Health Check (MEDIUM IMPACT)

**Arquivo:** `commons/postgres/postgres.go`

- Adicionar constante `ErrNotConnected = errors.New("connection not established")`
- Implementar método `Ping(ctx context.Context) error`
- Configurar `ConnMaxIdleTime` se não existir (5 minutos)
- Adicionar testes unitários em `commons/postgres/postgres_test.go`

### 3. M2 - MongoDB Connection Pool Health Check (MEDIUM IMPACT)

**Arquivo:** `commons/mongo/mongo.go`

- Adicionar constante `ErrNotConnected`
- Implementar método `Ping(ctx context.Context) error` usando `readpref.Primary()`
- Verificar/ajustar `ServerSelectionTimeout` (5 segundos) e `HeartbeatInterval` (10 segundos)
- Adicionar testes unitários em `commons/mongo/mongo_test.go`

### 4. L1 - Redis Connection Health Check (LOW IMPACT)

**Arquivo:** `commons/redis/redis.go`

- Adicionar constante `ErrNotConnected` se não existir
- Implementar método `Ping(ctx context.Context) error` thread-safe
- Adicionar testes unitários em `commons/redis/redis_test.go`

## Regras de Implementação

1. **Backward Compatibility:** Manter 100% - não alterar assinaturas de métodos existentes
2. **Padrões:** Seguir padrões de código existentes na lib (table-driven tests, double-check locking)
3. **Testes:** Adicionar testes unitários para todo código novo
4. **Validação:** Executar `go test ./... -v` e `go test ./... -race` após cada implementação

## Padrão de Erros

```go
var (
    ErrNotConnected = errors.New("connection not established")
)
```

## Padrão de Testes (table-driven)

```go
func TestXxx(t *testing.T) {
    tests := []struct {
        name        string
        setup       func() *Connection
        expectError bool
    }{
        // casos de teste
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // execução
        })
    }
}
```

## Critérios de Aceitação

- [ ] Todos os testes passam (incluindo novos)
- [ ] `go test -race` não reporta data races
- [ ] Código segue padrões existentes do projeto
- [ ] Backward compatibility mantida (sem breaking changes)
- [ ] Cobertura de testes adequada para código novo

## Arquivos a Modificar

| Arquivo | Alteração |
|---------|-----------|
| `commons/redis/redis.go` | Thread-safety em GetClient() + método Ping() |
| `commons/redis/redis_test.go` | Testes de concorrência e Ping |
| `commons/postgres/postgres.go` | Método Ping() + configuração de pool |
| `commons/postgres/postgres_test.go` | Testes de Ping |
| `commons/mongo/mongo.go` | Método Ping() + configurações de timeout |
| `commons/mongo/mongo_test.go` | Testes de Ping |
