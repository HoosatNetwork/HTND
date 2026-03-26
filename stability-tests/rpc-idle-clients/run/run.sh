#!/bin/bash
rm -rf /tmp/htnd-temp

NUM_CLIENTS=128
HTND --devnet --appdir=/tmp/htnd-temp --profile=6061 --rpcmaxwebsockets=$NUM_CLIENTS &
HOOSATD_PID=$!
HOOSATD_KILLED=0
function killHoosatdIfNotKilled() {
  if [ $HOOSATD_KILLED -eq 0 ]; then
    kill $HOOSATD_PID
  fi
}
trap "killHoosatdIfNotKilled" EXIT

sleep 1

rpc-idle-clients --devnet --profile=7000 -n=$NUM_CLIENTS
TEST_EXIT_CODE=$?

kill $HOOSATD_PID

wait $HOOSATD_PID
HOOSATD_EXIT_CODE=$?
HOOSATD_KILLED=1

echo "Exit code: $TEST_EXIT_CODE"
echo "Hoosatd exit code: $HOOSATD_EXIT_CODE"

if [ $TEST_EXIT_CODE -eq 0 ] && [ $HOOSATD_EXIT_CODE -eq 0 ]; then
  echo "rpc-idle-clients test: PASSED"
  exit 0
fi
echo "rpc-idle-clients test: FAILED"
exit 1
