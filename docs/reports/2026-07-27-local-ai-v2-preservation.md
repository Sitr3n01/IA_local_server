# Local AI v2 preservation checkpoint

Date: 2026-07-27, America/Sao_Paulo.

## Purpose

Preserve and locally version the existing Local AI v2 canary server without
changing the live runtime. The source repository remains `C:\IA\local-llama`.
The installed canary remains `C:\IA\local-ai-v2`.

This checkpoint is a canary preservation record, not a production promotion.
The final cutover gates remain the long soak, resource-headroom qualification,
and sustained observation described in the runbook and model-promotion docs.

## Git scope

- Repository: `C:\IA\local-llama`
- Branch for this checkpoint: `preserve/local-ai-v2-canary-20260727`
- Base HEAD before preservation: `b1e7a0a233e5`
- Remote configured at inspection time: none
- Included: Go source, PowerShell scripts, manifests, schemas, docs,
  sanitized integration configs, CI metadata, license/security project files,
  and this preservation report.
- Excluded: model weights, runtime caches, logs, state, installed binaries,
  generated runtime files under `C:\IA\local-ai-v2`, credentials, and local
  secret material.
- Excluded from the main checkpoint: `control/local_llama_panel.py`, because
  its current local diff is legacy forensic state and logs request headers/raw
  body failure captures. It must not be used as a production rollback surface.

## Live canary state observed

Scheduled tasks:

| Task | State |
| --- | --- |
| `CIA Local AI v2 Canary Edge` | Running |
| `CIA Local AI v2 Canary Router` | Running |

Loopback listeners:

| Address | Port | Role | Process ID observed |
| --- | ---: | --- | ---: |
| `127.0.0.1` | 18090 | data plane | 7120 |
| `127.0.0.1` | 18091 | control plane | 7120 |
| `127.0.0.1` | 19292 | router | 2176 |
| `127.0.0.1` | 8888 | Unsloth Studio | 9148 |

The canary data/control/router listeners are loopback-only. The source and
installed manifests currently list six canary candidates. The currently
documented selected/active boundary is still `local-coding`; catalog visibility
does not prove live deployment or generation for every discovered GGUF.

## Installed binary hashes

Files under `C:\IA\local-ai-v2\bin` observed before this checkpoint:

| File | SHA-256 | Size |
| --- | --- | ---: |
| `cia-credential.exe` | `E6DC0BD7646DFB8D4AF6CC59FAD7E4DA2FD05DC45C8E5B3ED4E3646FECFDD11E` | 2379264 |
| `cia-edge.exe` | `123A926B6F657895C5DA0F02518EEB735E238069CCA92A620A5E71864A87DCF9` | 11575808 |
| `cia-manifest.exe` | `DC86C4E312C29D50311C1484D6A7136017B1F6AE58A65012708B0C95F448A07A` | 3191808 |
| `cia-mcp-admin.exe` | `7330754D84C6BFC81100C590C2DA4F6817CADBB57D9E6BC4C33AD4515306B63E` | 12073984 |
| `cia-mcp-inference.exe` | `D2A6C4B64AB420B60186495C8194302D4CDEB7793CD6E99E339E968677B98813` | 12097024 |
| `cia-mcp.exe` | `9709D98AFF45208C020EF7C591474826DFE0E3ACE59E2A1347AF765FB8A4DAD4` | 11890176 |
| `cia-supervisor.exe` | `B747AA2E0FB8D7CA021008B574FBE0DC02A632BAE13617048DA0B22F6E8D9F65` | 3941888 |
| `cia-tray.exe` | `C5CEB1DACFA6C9074B90C47B0C7DA5052414789FE067252A7AFA5A16442AA95E` | 12435456 |
| `llama-swap.exe` | `60362E63EBAF97CF0DEC791986479022AB87FE7D1350A64D55B256821A437BAA` | 23479296 |

## Installed config hashes

Files under `C:\IA\local-ai-v2\config` observed before this checkpoint:

| File | SHA-256 | Size |
| --- | --- | ---: |
| `codex-model-catalog.json` | `D756F16B492899385EBB702A701526701D63109A4EDFCCBEF6B7124E0EE14CEC` | 22370 |
| `deployment.canary.json` | `C3E64B33AE1FF475A0656EF8A46A08C9B4C364440D1DD4F6A09B3A557C485667` | 2399 |
| `llama-swap.canary.yaml` | `E6454424922DAE90D9C14EBC557D6D0A668B49D9215170E1A9AA31180D3F4321` | 6719 |
| `models.schema.json` | `1452A8E5F84C9206DBB4A4C09B509541D4F561B38842C7554F5F71C4CBA98D1D` | 6590 |
| `models.source-snapshot.json` | `7E96562C3D2A1AC36D687FA184A1E80E134E252A737962C8E1343F50B11427D6` | 4422 |
| `models.yaml` | `8FF65F9796B14442FD6E248E22F15975F0FADF759EEA241BB3588B7120540276` | 9360 |
| `opencode.cia-local-canary.jsonc` | `BDE757000E0BC93D664ED57BBDEEC5DCEB1A9426F6223FD2262EDD80B95FB2FF` | 6161 |
| `panel.canary.json` | `9DABBA393EEAB0776982A6DBDA4F0A8EDEDD40E1D676DDBA493BD5E0594CDEC6` | 851 |

## Verification commands

Run these from `C:\IA\local-llama` before trusting the checkpoint:

```powershell
git diff --cached --check
go test ./...
.\scripts\v2\Test-V2Manifest.ps1 -VerifyArtifacts
.\scripts\v2\Test-V2HarnessConfig.ps1
.\scripts\v2\Test-V2Installation.ps1 -Environment Canary -VerifyHashes
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:18091/livez
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:18091/readyz
```

Do not print credential values while validating. Online generation tests are
intentionally outside this preservation gate because this checkpoint should not
load, switch, or qualify a model.
