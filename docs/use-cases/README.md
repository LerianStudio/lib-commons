# Commons-Go Use Cases

This directory contains real-world use cases demonstrating the benefits and migration paths for adopting the refactored commons-go library across different components of the Midaz ecosystem.

## Available Use Cases

### Midaz Backend Services

#### [📊 Observability Migration](./midaz/observability-migration.md)
**Core Services: Today vs Tomorrow**

Comprehensive migration guide for Midaz core services (Transaction, Onboarding) showing:
- **80% reduction** in observability setup code (50+ lines → 10 lines)
- **Automatic instrumentation** replacing manual span management
- **Unified database tracing** across all repositories  
- **Cross-service communication** with automatic trace propagation
- **Standardized business metrics** collection

**Key Benefits:** Faster development, consistent patterns, easier debugging

---

#### [🔌 Plugin Migration](./midaz/plugin-migration.md)
**Plugin Ecosystem Standardization**

Shows how commons-go transforms plugin development across the Midaz ecosystem:
- **90% reduction** in plugin observability code (120+ lines → 12 lines)
- **Standardized auth patterns** across all authentication flows
- **Customer journey tracking** automation in CRM plugin
- **Cross-plugin observability** for complex business processes
- **Unified monitoring dashboards** for entire plugin ecosystem

**Key Benefits:** Consistent plugin architecture, faster plugin development, unified monitoring

---

## Migration Impact Summary

| Component               | Current Complexity   | After Commons-Go  | Time Savings   | Risk Level |
| ----------------------- | -------------------- | ----------------- | -------------- | ---------- |
| **Transaction Service** | High (200+ LOC)      | Low (50 LOC)      | 2 days         | Low        |
| **Onboarding Service**  | High (180+ LOC)      | Low (45 LOC)      | 1.5 days       | Low        |
| **Auth Plugin**         | Very High (120+ LOC) | Very Low (12 LOC) | 1 day          | Low        |
| **CRM Plugin**          | High (100+ LOC)      | Low (25 LOC)      | 1 day          | Low        |
| **Other Plugins**       | Medium-High          | Low               | 3-5 days total | Low        |

**Total Migration Time:** ~5.5 days for core services + ~1 week for plugins  
**Overall Code Reduction:** 80-90% less observability boilerplate  
**Development Velocity Improvement:** 60% faster feature development

## Benefits Overview

### Immediate Benefits
- 🚀 **Development Velocity**: Focus on business logic, not observability plumbing
- 🐛 **Reduced Bugs**: Consistent patterns eliminate configuration errors  
- 📊 **Unified Metrics**: Same metrics and naming across all services
- 🔍 **Better Debugging**: Automatic trace correlation and error tracking
- 🧪 **Easier Testing**: Standardized mocking and test utilities

### Long-term Benefits  
- 🔧 **Easier Maintenance**: Single library to update for observability features
- 📈 **Platform Insights**: Automatic performance monitoring across entire platform
- 🎯 **Better Alerting**: Consistent metric names enable sophisticated alerting rules
- 👥 **Team Productivity**: Onboarding new developers faster with standard patterns
- 🏢 **Platform Evolution**: Easy to add new observability features across all components

## Getting Started

1. **Review the use cases** relevant to your component
2. **Understand the migration patterns** specific to your service type
3. **Follow the phase-based approach** outlined in each use case
4. **Leverage the team knowledge** captured in these real-world examples

Each use case provides concrete code examples, quantified benefits, and step-by-step migration guidance to ensure smooth adoption of the commons-go library across the Midaz platform. 