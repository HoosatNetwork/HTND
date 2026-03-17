#!/bin/bash
set -e

BASEDIR=$(dirname "$0")
PROJECT_ROOT=$( cd "${BASEDIR}/.."; pwd)
SKIP_LONG_STABILITY_TESTS=${SKIP_LONG_STABILITY_TESTS:-}

failedTests=()

# echo "Running application-level-garbage"
# cd "${PROJECT_ROOT}/application-level-garbage/run" && ./run.sh || failedTests+=("application-level-garbage")
# echo "Done running application-level-garbage"

echo "Running infra-level-garbage"
cd "${PROJECT_ROOT}/infra-level-garbage/run" && ./run.sh || failedTests+=("infra-level-garbage")
echo "Done running infra-level-garbage"

echo "Running htndsanity"
cd "${PROJECT_ROOT}/htndsanity/run" && ./run.sh || failedTests+=("htndsanity")
echo "Done running htndsanity"

echo "Running rpc-stability"
cd "${PROJECT_ROOT}/rpc-stability/run" && ./run.sh || failedTests+=("rpc-stability")
echo "Done running rpc-stability"

echo "Running rpc-idle-clients"
cd "${PROJECT_ROOT}/rpc-idle-clients/run" && ./run.sh || failedTests+=("rpc-idle-clients")
echo "Done running rpc-idle-clients"

echo "Running simple-sync"
cd "${PROJECT_ROOT}/simple-sync/run" && ./run.sh || failedTests+=("simple-sync")
echo "Done running simple-sync"

echo "Running reorg"
cd "${PROJECT_ROOT}/reorg/run" && ./run.sh || failedTests+=("reorg")
echo "Done running reorg"

echo "Running many-tips"
cd "${PROJECT_ROOT}/many-tips/run" && ./run.sh || failedTests+=("many-tips")
echo "Done running many-tips"

echo "Running netsync - fast"
cd "${PROJECT_ROOT}/netsync/run" && ./run-fast.sh || failedTests+=("netsync")
echo "Done running netsync - fast"

if [ -n "${SKIP_LONG_STABILITY_TESTS}" ]; then
  echo "Skipping long stability tests in CI: orphans "
else
  echo "Running orphans"
  cd "${PROJECT_ROOT}/orphans/run" && ./run.sh || failedTests+=("orphans")
  echo "Done running orphans"



fi


EXIT_CODE=0
for t in "${failedTests[@]}"; do
  EXIT_CODE=1
  echo "FAILED: ${t}"
done

echo "Exiting with: ${EXIT_CODE}"
exit $EXIT_CODE