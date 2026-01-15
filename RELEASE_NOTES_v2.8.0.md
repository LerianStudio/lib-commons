# Release Notes - lib-commons v2.8.0

## Visão Geral

Esta versão foca na **eliminação de `panic` e `log.Fatal`** em favor de retornos de erro adequados, seguindo as melhores práticas de Go para tratamento de erros. Todas as alterações mantêm **100% de backward compatibility**.

---

## Alterações

### 1. Circuit Breaker Health Checker

**O que mudou:**

- **Arquivos:** `commons/circuitbreaker/healthchecker.go`, `commons/circuitbreaker/healthchecker_test.go`
- Adicionadas variáveis de erro exportadas: `ErrInvalidHealthCheckInterval`, `ErrInvalidHealthCheckTimeout`
- Nova função `NewHealthCheckerWithValidation()` que retorna `(HealthChecker, error)`
- Função `NewHealthChecker()` marcada como deprecated, agora usa a nova função internamente

```go
// Antes (panic em caso de erro)
hc := circuitbreaker.NewHealthChecker(manager, interval, timeout, logger)

// Depois (tratamento de erro explícito)
hc, err := circuitbreaker.NewHealthCheckerWithValidation(manager, interval, timeout, logger)
if err != nil {
    return fmt.Errorf("failed to create health checker: %w", err)
}
```

**Por que mudou:**

- Eliminação de `panic` permite que o chamador decida como tratar erros de configuração
- Erros exportados permitem uso de `errors.Is()` para verificação específica
- Backward compatibility mantido através da função deprecated

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Função antiga continua funcionando normalmente. Nova função disponível para quem deseja tratamento de erro explícito. |

**Ação necessária:**

Migração opcional, mas recomendada para novos desenvolvimentos.

**Rollback:**

Não há necessidade. A função `NewHealthChecker()` continua funcionando normalmente.

---

### 2. Context WithTimeout

**O que mudou:**

- **Arquivos:** `commons/context.go`, `commons/context_test.go`
- Adicionada variável de erro exportada: `ErrNilParentContext`
- Nova função `WithTimeoutSafe()` que retorna `(context.Context, context.CancelFunc, error)`

```go
// Nova função segura
ctx, cancel, err := commons.WithTimeoutSafe(parentCtx, timeout)
if err != nil {
    return fmt.Errorf("failed to create context: %w", err)
}
defer cancel()
```

**Por que mudou:**

- Evita panic quando contexto pai é nil
- Permite tratamento gracioso de erros de configuração
- Facilita debugging em ambientes de produção

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Nova função adicional. Nenhuma alteração nas funções existentes. |

**Ação necessária:**

Migração opcional. Considere usar `WithTimeoutSafe()` em código novo.

**Rollback:**

Não aplicável. Função original não foi alterada.

---

### 3. OS Config

**O que mudou:**

- **Arquivos:** `commons/os.go`
- Função `EnsureConfigFromEnvVars()` marcada como deprecated com comentário indicando alternativa

**Por que mudou:**

- Indica aos desenvolvedores que devem migrar para alternativas mais seguras
- Prepara o terreno para remoção futura da função

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Apenas adição de marcação deprecated. Função continua operacional. |

**Ação necessária:**

Verificar documentação para alternativa recomendada.

**Rollback:**

Não há necessidade. Função continua funcionando normalmente.

---

### 4. License Manager

**O que mudou:**

- **Arquivos:** `commons/license/manager.go`, `commons/license/manager_test.go`
- Adicionadas variáveis de erro exportadas: `ErrLicenseValidationFailed`, `ErrManagerNotInitialized`
- Nova função `DefaultHandlerWithError()` que retorna `error` em vez de chamar `os.Exit()`
- Novo método `TerminateWithError()` que retorna `error` em vez de chamar `os.Exit()`
- Funções originais marcadas como deprecated

```go
// Antes (os.Exit em caso de erro)
license.DefaultHandler(reason)

// Depois (tratamento de erro explícito)
if err := license.DefaultHandlerWithError(reason); err != nil {
    log.Errorf("License validation failed: %v", err)
    return err
}
```

**Por que mudou:**

- Chamadas a `os.Exit()` impedem cleanup adequado de recursos
- Dificulta testes unitários
- Novas funções permitem que o chamador decida a ação apropriada

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Funções antigas continuam funcionando. Novas funções disponíveis para melhor controle. |

**Ação necessária:**

Migração recomendada para código que necessita de cleanup antes de terminar.

**Rollback:**

Não há necessidade. Funções deprecated continuam funcionando normalmente.

---

### 5. Zap Logger

**O que mudou:**

- **Arquivos:** `commons/zap/injector.go`, `commons/zap/injector_test.go`
- Nova função `InitializeLoggerWithError()` que retorna `(log.Logger, error)`
- Função `InitializeLogger()` marcada como deprecated

```go
// Antes (panic em caso de erro)
logger := zap.InitializeLogger()

// Depois (tratamento de erro explícito)
logger, err := zap.InitializeLoggerWithError()
if err != nil {
    return fmt.Errorf("failed to initialize logger: %w", err)
}
```

**Por que mudou:**

- Panic durante inicialização do logger impede tratamento adequado de falhas
- Nova função permite fallback para logger padrão ou outra estratégia

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Função original continua funcionando. Nova função oferece controle adicional. |

**Ação necessária:**

Migração opcional, mas recomendada para aplicações que necessitam de alta disponibilidade.

**Rollback:**

Não há necessidade. `InitializeLogger()` continua funcionando normalmente.

---

### 6. PostgreSQL Connection

**O que mudou:**

- **Arquivos:** `commons/postgres/postgres.go`
- Todas as chamadas `Fatal/Fatalf` substituídas por `Error` + `return fmt.Errorf()`
- Erros são propagados corretamente para o chamador

**Por que mudou:**

- `log.Fatal` termina o processo imediatamente sem cleanup
- Propagação de erros permite retry, fallback ou shutdown gracioso
- Melhora testabilidade do código

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Comportamento externo não muda para chamadores que já tratam erros retornados. |

**Ação necessária:**

Verificar se o código chamador trata adequadamente os erros retornados.

**Rollback:**

Não aplicável. Alteração interna de logging.

---

### 7. MongoDB Connection

**O que mudou:**

- **Arquivos:** `commons/mongo/mongo.go`
- Chamada `Fatal` substituída por `Error` + `return fmt.Errorf()`
- Erro é propagado corretamente para o chamador

**Por que mudou:**

- Consistência com outras conexões de banco de dados
- Permite tratamento adequado de falhas de conexão
- Facilita implementação de retry logic

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Comportamento externo não muda para chamadores que já tratam erros retornados. |

**Ação necessária:**

Verificar se o código chamador trata adequadamente os erros retornados.

**Rollback:**

Não aplicável. Alteração interna de logging.

---

### 8. RabbitMQ Connection

**O que mudou:**

- **Arquivos:** `commons/rabbitmq/rabbitmq.go`
- Todas as chamadas `Fatal/Fatalf` substituídas por `Error/Errorf` + `return fmt.Errorf()`
- Erros são propagados corretamente para o chamador

**Por que mudou:**

- Consistência com outras conexões
- Permite implementação de circuit breaker ou retry
- Facilita shutdown gracioso em caso de falha

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Comportamento externo não muda para chamadores que já tratam erros retornados. |

**Ação necessária:**

Verificar se o código chamador trata adequadamente os erros retornados.

**Rollback:**

Não aplicável. Alteração interna de logging.

---

### 9. Server Manager

**O que mudou:**

- **Arquivos:** `commons/server/shutdown.go`, `commons/server/shutdown_test.go`
- Adicionada variável de erro exportada: `ErrNoServersConfigured`
- Novo método `StartWithGracefulShutdownWithError()` que retorna `error` em vez de chamar `Fatal`
- Adicionado método privado `validateConfiguration()` para validação

```go
// Antes (Fatal em caso de erro)
sm.StartWithGracefulShutdown()

// Depois (tratamento de erro explícito)
if err := sm.StartWithGracefulShutdownWithError(); err != nil {
    return fmt.Errorf("failed to start servers: %w", err)
}
```

**Por que mudou:**

- Permite tratamento de erros de configuração antes de iniciar servidores
- Facilita testes de integração
- Erro exportado permite verificação específica com `errors.Is()`

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Método original continua funcionando. Novo método oferece controle adicional. |

**Ação necessária:**

Migração recomendada para ambientes que necessitam de tratamento de erro na inicialização.

**Rollback:**

Não há necessidade. `StartWithGracefulShutdown()` continua funcionando normalmente.

---

### 10. OpenTelemetry

**O que mudou:**

- **Arquivos:** `commons/opentelemetry/otel.go`, `commons/opentelemetry/otel_test.go`
- Nova função `InitializeTelemetryWithError()` que retorna `(*Telemetry, error)`
- Função `InitializeTelemetry()` marcada como deprecated

```go
// Antes (panic em caso de erro)
telemetry := opentelemetry.InitializeTelemetry(cfg)

// Depois (tratamento de erro explícito)
telemetry, err := opentelemetry.InitializeTelemetryWithError(cfg)
if err != nil {
    return fmt.Errorf("failed to initialize telemetry: %w", err)
}
```

**Por que mudou:**

- Panic durante inicialização de telemetria pode derrubar toda a aplicação
- Nova função permite degradação graciosa (rodar sem telemetria)
- Facilita testes e debugging

**Impacto:**

| Risco | Descrição |
|-------|-----------|
| Baixo | Função original continua funcionando. Nova função oferece controle adicional. |

**Ação necessária:**

Migração recomendada para aplicações que podem operar sem telemetria.

**Rollback:**

Não há necessidade. `InitializeTelemetry()` continua funcionando normalmente.

---

## Resumo

| Métrica | Valor |
|---------|-------|
| Total de melhorias | 10 |
| Backward compatibility | 100% |
| Funções deprecated | Continuam funcionando |
| Novas funções | Sufixo `WithError` ou `WithValidation` |

### Novas Variáveis de Erro Exportadas

| Pacote | Erro |
|--------|------|
| `circuitbreaker` | `ErrInvalidHealthCheckInterval`, `ErrInvalidHealthCheckTimeout` |
| `commons` | `ErrNilParentContext` |
| `license` | `ErrLicenseValidationFailed`, `ErrManagerNotInitialized` |
| `server` | `ErrNoServersConfigured` |

### Uso com `errors.Is()`

```go
hc, err := circuitbreaker.NewHealthCheckerWithValidation(manager, interval, timeout, logger)
if errors.Is(err, circuitbreaker.ErrInvalidHealthCheckInterval) {
    // Tratamento específico para intervalo inválido
}
```

### Recomendações

1. **Novos desenvolvimentos**: Utilizar as novas funções com sufixo `WithError` ou `WithValidation`
2. **Código existente**: Migração opcional, mas recomendada para melhor tratamento de erros
3. **Testes**: Aproveitar as novas funções para melhorar cobertura de testes
4. **Monitoramento**: Erros exportados facilitam métricas específicas por tipo de falha
