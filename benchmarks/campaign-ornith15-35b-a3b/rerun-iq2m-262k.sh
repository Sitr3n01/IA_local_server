#!/usr/bin/env bash
# Re-runs the one unit that crashed: iq2m retention at 262144. Waits for the
# Qwen3.6 driver to release the GPU first, and captures stderr this time -
# the first attempt stored only the traceback's opening line, which is why
# the cause is still unknown.
CAMP="C:/IA/IA_local_server/benchmarks/campaign-ornith15-35b-a3b"
DEADLINE_EPOCH=$(date -d "2026-08-25 11:00" +%s 2>/dev/null || echo 0)

while [ ! -f "$CAMP/qwen36-retention-driver.DONE" ]; do
  [ "$(date +%s)" -ge "$DEADLINE_EPOCH" ] && { echo "SKIP: deadline reached before the GPU was free"; exit 0; }
  sleep 120
done
while [ "$(powershell.exe -NoProfile -Command '@(Get-Process llama-server -ErrorAction SilentlyContinue).Count' | tr -d '\r')" != "0" ]; do sleep 20; done

now=$(date +%s)
if [ "$now" -ge "$DEADLINE_EPOCH" ]; then echo "SKIP: past deadline"; exit 0; fi
echo "RERUN starting $(date)"

powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "& 'C:\IA\IA_local_server\scripts\v2\Invoke-V2ProfileQualification.ps1' -RuntimeRoot 'C:\IA\runtimes\llama.cpp\b10549-rocm-7.14' -ModelPath 'C:\IA\models\Ornith-1.5-35B-A3B-GGUF\Ornith-1.5-35B-A3B-IQ2_M.gguf' -Label 'iq2m-retention-ctx262144-ncpumoe6-retry' -ContextTokens 262144 -CacheTypeK q4_0 -CacheTypeV q4_0 -NCpuMoe 6 -Suites retention -RetentionTokens @(32768,81920,131072,180224,240000) -CaptureDiagnostics -DeviceVramMib 16304 -OutputRoot '$CAMP\qualification'" > "$CAMP/qualification/rerun-iq2m-262k.out" 2>&1
echo "RERUN exit=$? at $(date)"
tail -5 "$CAMP/qualification/rerun-iq2m-262k.out" | tr -d '\0'
