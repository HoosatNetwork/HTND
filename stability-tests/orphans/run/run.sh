#!/bin/bash
rm -rf /tmp/htnd-temp

HTND --simnet --appdir=/tmp/htnd-temp --rpclisten 127.0.0.1:42420 --listen 127.0.0.1:42421 --profile=6061 &
HOOSATD_PID=$!

sleep 1

orphans --simnet --rpcserver 127.0.0.1:42420 -a127.0.0.1:42421 -n20 --profile=7000
TEST_EXIT_CODE=$?

kill $HOOSATD_PID

wait $HOOSATD_PID
HOOSATD_EXIT_CODE=$?

echo "Exit code: $TEST_EXIT_CODE"
echo "Hoosatd exit code: $HOOSATD_EXIT_CODE"

if [ $TEST_EXIT_CODE -eq 0 ] && [ $HOOSATD_EXIT_CODE -eq 0 ]; then
  echo "orphans test: PASSED"
  exit 0
fi
echo "orphans test: FAILED"
exit 1
