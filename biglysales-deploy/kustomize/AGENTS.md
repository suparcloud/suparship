# Agent Configuration Rules

This document outlines the rules and guidelines for configuring agent resources in the kustomize deployment configurations.

## Memory Resource Configuration

### Scope (Applicability)

These memory rules apply differently by container type:

- **Agent server only:** For the agent server container (`voiceai-livekit-agent-server`), memory **requests and limits must be equal**.
- **All other containers (cm, web, sidecars):** Capacity manager (cm), web (`voiceai-livekit-web`), and sidecars/init containers (e.g. `signal-watcher`, `wait-for-node-stability`). For these, requests and limits **may differ**; the difference must be **at most 100Mi** (i.e. `|limit - request| <= 100Mi`).

### Memory Settings

**Memory Requests and Limits:**
- **Value:** `2230Mi` for both requests and limits
- **Rationale:** On production, max usage is 1560Mi. The request is set to make this value 70% of max, resulting in 2230Mi.
- **Round** after you get the proper memory request number, round up to the next 10Mi **only when the computed value is greater than 300Mi** (e.g. 2228→2230, 6972→6980). For values ≤ 300Mi, do not require rounding to 10Mi (e.g. 250Mi, 65Mi can stay as-is).
- **Staging** in base/staging use exact max usage prod value 1560Mi (coeff 1), instead of max/0.7, this way we catch memory issues earlier on staging and use less resources.
- **Canary** for canary use value coefficient 0.9, eg. max/0.9 (1560/0.9=1740 after apply round)

**Comment Format (required only when memory value > 500Mi):**
- Above memory request line:
  ```yaml
  # on prod max usage 1560, set request to make this value 70% of max: 2230Mi
  memory: "2230Mi"
  ```
- Above memory limit line:
  ```yaml
  # same as requests to guaranteed memory and avoid node level OOM
  memory: "2230Mi"
  ```
- For memory ≤ 500Mi (e.g. sidecars or small main-container values), rationale comments are optional.

**Comment Placement:**
- When comments are required, place them directly above the `memory:` line, not above the `requests:` or `limits:` section.

### Handle Memory increase request
In case asked to add some amount of memory (typically 500Mi) to specific agent:
- preserve all current comments
- add a comment only for memory request, describing the actual reason for the increase:
  - OOM kill: `# OOM on <current memory limit value>, add <delta>`
  - Alert threshold (e.g. 80% usage): `# 80% memory alert on <current memory limit value>, add <delta>`
  - Use the reason the user states; do NOT default to "OOM" if the cause was an alert
- add the same amount to request and limits
- preserve memory request and limits for the agents.

## Environment-Specific Configuration

### Staging Environment
- **Use base configuration directly** - Do not create patches for resources
- Resource values should be defined in the base files
- Staging patches should only contain environment-specific overrides (e.g., health checks, env vars)

### Capacity Manager (cm) env rules

- **`CM__MAX_AGENT_SERVER_CAPACITY` must equal `MAX_AGENTS`**: keep capacity-manager and agent-server in sync so dispatch/load calculations match actual server capacity.

### Production Environment
- **Use patches for resources** - Resource values should be overridden via patches
- Patches should be placed in `overlays/prod/` directories
- Patches allow for environment-specific resource tuning without modifying base configurations

### Canary Environment
- **Use patches for resources** - Resource values should be overridden via patches
- Patches should be placed in `overlays/prod/` directories (for canary-livekit-sh)
- Follows the same pattern as production patches

## File Structure

### Express Caller Configuration Files
- **Base:** `express-caller/base/livekit-agent.yaml`
- **Production Patch:** `express-caller/overlays/prod/patch-livekit-agent.yaml`
- **Staging Patch:** `express-caller/overlays/staging/patch-livekit-agent.yaml` (no resource patches)
- **Canary Patch:** `canary-livekit-sh/overlays/prod/patch-express-caller.yaml`

## Example Configuration

### Base File (for staging)
```yaml
resources:
  requests:
    cpu: "2"
    # on prod max usage 1560, set request to make this value 70% of max: 2230Mi
    memory: "2230Mi"
  limits:
    # same as requests to guaranteed memory and avoid node level OOM
    memory: "2230Mi"
```

### Production/Canary Patch
```yaml
resources:
  requests:
    cpu: "2"
    # on prod max usage 1560, set request to make this value 70% of max: 2230Mi
    memory: "2230Mi"
  limits:
    # same as requests to guaranteed memory and avoid node level OOM
    memory: "2230Mi"
```

## Key Principles

1. **Memory requests and limits should match for the agent server container only** - This guarantees memory allocation and avoids node-level OOM kills. For cm, web, and sidecars, request and limit may differ; the difference must be at most 100Mi.
2. **Comments explain the rationale for memory values larger than 500Mi** - Include comments explaining why specific values are chosen when the memory value is > 500Mi.
3. **Staging uses base, prod/canary use patches** - Maintains separation of concerns and allows independent tuning
4. **Comments placed above memory lines** - Ensures comments are directly associated with the values they describe (when comments are required per rule 2)
