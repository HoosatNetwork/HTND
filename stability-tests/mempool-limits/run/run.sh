#!/bin/bash

APPDIR=/tmp/htnd-temp
HOOSATD_RPC_PORT=29587

rm -rf "${APPDIR}"

HTND --simnet --appdir="${APPDIR}" --rpclisten=0.0.0.0:"${HOOSATD_RPC_PORT}" --profile=6061 &
HOOSATD_PID=$!

sleep 1

RUN_STABILITY_TESTS=true go test ../ -v -timeout 86400s -- --rpc-address=127.0.0.1:"${HOOSATD_RPC_PORT}" --profile=7000
TEST_EXIT_CODE=$?

kill $HOOSATD_PID

wait $HOOSATD_PID
HOOSATD_EXIT_CODE=$?

echo "Exit code: $TEST_EXIT_CODE"
echo "Hoosatd exit code: $HOOSATD_EXIT_CODE"

if [ $TEST_EXIT_CODE -eq 0 ] && [ $HOOSATD_EXIT_CODE -eq 0 ]; then
  echo "mempool-limits test: PASSED"
  exit 0
fi
echo "mempool-limits test: FAILED"
exit 1
