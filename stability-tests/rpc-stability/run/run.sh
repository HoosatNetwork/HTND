#!/bin/bash
rm -rf /tmp/htnd-temp

HTND --devnet --appdir=/tmp/htnd-temp --profile=6061 --loglevel=debug &
HOOSATD_PID=$!

sleep 1

rpc-stability --devnet -p commands.json --profile=7000
TEST_EXIT_CODE=$?

kill $HOOSATD_PID

wait $HOOSATD_PID
HOOSATD_EXIT_CODE=$?

echo "Exit code: $TEST_EXIT_CODE"
echo "Hoosatd exit code: $HOOSATD_EXIT_CODE"

if [ $TEST_EXIT_CODE -eq 0 ] && [ $HOOSATD_EXIT_CODE -eq 0 ]; then
  echo "rpc-stability test: PASSED"
  exit 0
fi
echo "rpc-stability test: FAILED"
exit 1
