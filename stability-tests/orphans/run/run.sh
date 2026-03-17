#!/bin/bash
rm -rf /tmp/htnd-temp

HTND --simnet --appdir=/tmp/htnd-temp --rpclisten localhost:16510 --listen localhost:16511 --profile=6061 &
HOOSATD_PID=$!

sleep 1

orphans --simnet -alocalhost:16511 -n20 --profile=7000
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
