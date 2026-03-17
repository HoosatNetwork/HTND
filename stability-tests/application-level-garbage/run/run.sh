#!/bin/bash
rm -rf /tmp/htnd-temp

HTND --devnet --appdir=/tmp/htnd-temp --profile=6061 --loglevel=debug &
HOOSATD_PID=$!
HOOSATD_KILLED=0
function killHoosatdIfNotKilled() {
    if [ $HOOSATD_KILLED -eq 0 ]; then
      kill $HOOSATD_PID
    fi
}
trap "killHoosatdIfNotKilled" EXIT

sleep 1

application-level-garbage --devnet -alocalhost:16611 -b blocks.dat --profile=7000
TEST_EXIT_CODE=$?

kill $HOOSATD_PID

wait $HOOSATD_PID
HOOSATD_KILLED=1
HOOSATD_EXIT_CODE=$?

echo "Exit code: $TEST_EXIT_CODE"
echo "Hoosatd exit code: $HOOSATD_EXIT_CODE"

if [ $TEST_EXIT_CODE -eq 0 ] && [ $HOOSATD_EXIT_CODE -eq 0 ]; then
  echo "application-level-garbage test: PASSED"
  exit 0
fi
echo "application-level-garbage test: FAILED"
exit 1
