# Plano de Testes Local - lib-commons v2.8.0

## Pré-requisitos

### Configuração do Ambiente
1. **Apontar versão local da lib-commons** no serviço consumidor:
   ```go
   // go.mod do serviço consumidor
   replace github.com/LerianStudio/lib-commons => ../lib-commons
   ```

2. **Dependências de infraestrutura** (para testes de conexão):
   - PostgreSQL rodando localmente (porta 5432)
   - MongoDB rodando localmente (porta 27017)
   - RabbitMQ rodando localmente (porta 5672)

---

## Cenários de Teste

### 1. Circuit Breaker Health Checker

#### ✅ Cenário de Sucesso
```go
manager := circuitbreaker.NewManager()
interval := 5 * time.Second
timeout := 2 * time.Second
logger := yourLogger

hc, err := circuitbreaker.NewHealthCheckerWithValidation(manager, interval, timeout, logger)

// Esperado: err == nil, hc != nil
```

#### ❌ Cenários de Erro
```go
// Teste 1: Intervalo inválido (zero ou negativo)
hc, err := circuitbreaker.NewHealthCheckerWithValidation(manager, 0, timeout, logger)
// Esperado: errors.Is(err, circuitbreaker.ErrInvalidHealthCheckInterval) == true

// Teste 2: Timeout inválido
hc, err := circuitbreaker.NewHealthCheckerWithValidation(manager, interval, 0, logger)
// Esperado: errors.Is(err, circuitbreaker.ErrInvalidHealthCheckTimeout) == true
```

---

### 2. Context WithTimeout

#### ✅ Cenário de Sucesso
```go
parentCtx := context.Background()
timeout := 5 * time.Second

ctx, cancel, err := commons.WithTimeoutSafe(parentCtx, timeout)
defer cancel()

// Esperado: err == nil, ctx != nil
```

#### ❌ Cenário de Erro
```go
// Contexto pai nil
ctx, cancel, err := commons.WithTimeoutSafe(nil, 5*time.Second)

// Esperado: errors.Is(err, commons.ErrNilParentContext) == true
```

---

### 3. License Manager

#### ✅ Cenário de Sucesso
```go
// Teste com DefaultHandlerWithError
err := license.DefaultHandlerWithError("valid_license_reason")
// Esperado: verificar comportamento (depende da implementação)

// Teste com TerminateWithError
err := manager.TerminateWithError()
// Esperado: err == nil ou erro tratável
```

#### ❌ Cenários de Erro
```go
// Manager não inicializado
var manager *license.Manager
err := manager.TerminateWithError()
// Esperado: errors.Is(err, license.ErrManagerNotInitialized) == true

// Falha na validação
err := license.DefaultHandlerWithError("invalid_reason")
// Esperado: errors.Is(err, license.ErrLicenseValidationFailed) == true
```

---

### 4. Zap Logger

#### ✅ Cenário de Sucesso
```go
logger, err := zap.InitializeLoggerWithError()

// Esperado: err == nil, logger != nil
logger.Info("Test message")
```

#### ❌ Cenário de Erro
```go
// Simular falha (ex: configuração inválida via env vars)
os.Setenv("LOG_LEVEL", "invalid_level")
logger, err := zap.InitializeLoggerWithError()

// Esperado: err != nil (verificar mensagem de erro)
```

---

### 5. PostgreSQL Connection

#### ✅ Cenário de Sucesso
```go
// Configurar conexão válida
cfg := postgres.Config{
    Host:     "localhost",
    Port:     5432,
    User:     "postgres",
    Password: "postgres",
    Database: "testdb",
}

conn, err := postgres.Connect(cfg)

// Esperado: err == nil, conn != nil
```

#### ❌ Cenários de Erro
```go
// Host inválido
cfg := postgres.Config{Host: "invalid_host", ...}
conn, err := postgres.Connect(cfg)
// Esperado: err != nil (conexão falhou, mas sem panic/fatal)

// Porta errada
cfg := postgres.Config{Port: 9999, ...}
conn, err := postgres.Connect(cfg)
// Esperado: err != nil (aplicação continua rodando)

// Credenciais inválidas
cfg := postgres.Config{Password: "wrong_password", ...}
conn, err := postgres.Connect(cfg)
// Esperado: err != nil (erro tratável)
```

---

### 6. MongoDB Connection

#### ✅ Cenário de Sucesso
```go
cfg := mongo.Config{
    URI:      "mongodb://localhost:27017",
    Database: "testdb",
}

client, err := mongo.Connect(cfg)

// Esperado: err == nil, client != nil
```

#### ❌ Cenários de Erro
```go
// URI inválida
cfg := mongo.Config{URI: "mongodb://invalid_host:27017", ...}
client, err := mongo.Connect(cfg)
// Esperado: err != nil (sem panic/fatal)

// MongoDB não disponível (parar o serviço)
client, err := mongo.Connect(cfg)
// Esperado: err != nil (aplicação continua rodando)
```

---

### 7. RabbitMQ Connection

#### ✅ Cenário de Sucesso
```go
cfg := rabbitmq.Config{
    URL: "amqp://guest:guest@localhost:5672/",
}

conn, err := rabbitmq.Connect(cfg)

// Esperado: err == nil, conn != nil
```

#### ❌ Cenários de Erro
```go
// URL inválida
cfg := rabbitmq.Config{URL: "amqp://invalid_host:5672/"}
conn, err := rabbitmq.Connect(cfg)
// Esperado: err != nil (sem panic/fatal)

// Credenciais erradas
cfg := rabbitmq.Config{URL: "amqp://wrong:wrong@localhost:5672/"}
conn, err := rabbitmq.Connect(cfg)
// Esperado: err != nil (aplicação continua rodando)
```

---

### 8. Server Manager

#### ✅ Cenário de Sucesso
```go
sm := server.NewManager()
sm.AddServer(httpServer)

err := sm.StartWithGracefulShutdownWithError()

// Esperado: servidor inicia normalmente
```

#### ❌ Cenário de Erro
```go
// Nenhum servidor configurado
sm := server.NewManager()
err := sm.StartWithGracefulShutdownWithError()

// Esperado: errors.Is(err, server.ErrNoServersConfigured) == true
```

---

### 9. OpenTelemetry

#### ✅ Cenário de Sucesso
```go
cfg := opentelemetry.Config{
    ServiceName: "test-service",
    Endpoint:    "localhost:4317",
}

telemetry, err := opentelemetry.InitializeTelemetryWithError(cfg)

// Esperado: err == nil, telemetry != nil
```

#### ❌ Cenário de Erro
```go
// Configuração inválida
cfg := opentelemetry.Config{ServiceName: ""}
telemetry, err := opentelemetry.InitializeTelemetryWithError(cfg)
// Esperado: err != nil (sem panic)

// Endpoint inacessível
cfg := opentelemetry.Config{Endpoint: "invalid:9999"}
telemetry, err := opentelemetry.InitializeTelemetryWithError(cfg)
// Esperado: err != nil (aplicação continua)
```

---

## Checklist de Validação

| Componente | Função Antiga (deprecated) | Nova Função | Sucesso | Erro |
|------------|---------------------------|-------------|---------|------|
| Circuit Breaker | `NewHealthChecker()` | `NewHealthCheckerWithValidation()` | ☐ | ☐ |
| Context | - | `WithTimeoutSafe()` | ☐ | ☐ |
| License Manager | `DefaultHandler()` | `DefaultHandlerWithError()` | ☐ | ☐ |
| License Manager | `Terminate()` | `TerminateWithError()` | ☐ | ☐ |
| Zap Logger | `InitializeLogger()` | `InitializeLoggerWithError()` | ☐ | ☐ |
| PostgreSQL | - | `Connect()` (erro propagado) | ☐ | ☐ |
| MongoDB | - | `Connect()` (erro propagado) | ☐ | ☐ |
| RabbitMQ | - | `Connect()` (erro propagado) | ☐ | ☐ |
| Server Manager | `StartWithGracefulShutdown()` | `StartWithGracefulShutdownWithError()` | ☐ | ☐ |
| OpenTelemetry | `InitializeTelemetry()` | `InitializeTelemetryWithError()` | ☐ | ☐ |

---

## Validações Críticas

### 1. Sem Panic/Fatal
Para cada cenário de erro, verificar que:
- ☐ A aplicação **NÃO** termina abruptamente
- ☐ O erro é retornado ao chamador
- ☐ É possível fazer tratamento adequado (log, retry, fallback)

### 2. Backward Compatibility
Verificar que funções deprecated continuam funcionando:
```go
// Deve funcionar sem alterações
hc := circuitbreaker.NewHealthChecker(...)  // deprecated mas funcional
logger := zap.InitializeLogger()             // deprecated mas funcional
sm.StartWithGracefulShutdown()              // deprecated mas funcional
```

### 3. errors.Is() com Erros Exportados
```go
// Verificar que errors.Is funciona corretamente
if errors.Is(err, circuitbreaker.ErrInvalidHealthCheckInterval) { ... }
if errors.Is(err, commons.ErrNilParentContext) { ... }
if errors.Is(err, license.ErrManagerNotInitialized) { ... }
if errors.Is(err, server.ErrNoServersConfigured) { ... }
```

---

## Ordem Recomendada de Execução

1. **Zap Logger** - base para logs dos demais testes
2. **Context** - dependência básica
3. **PostgreSQL/MongoDB/RabbitMQ** - conexões de infraestrutura
4. **Circuit Breaker** - depende de conexões
5. **OpenTelemetry** - observabilidade
6. **License Manager** - validação de licença
7. **Server Manager** - inicialização do servidor
