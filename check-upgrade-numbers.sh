#!/bin/sh

UPGRADES=`grep -ho 'upgrades.[0-9][0-9]*.' database/upgrades/* | sort | wc -l`
UPGRADES_IDS=`grep -ho 'upgrades.[0-9][0-9]*.' database/upgrades/* | sort | uniq | wc -l`

if [ $UPGRADES -ne $UPGRADES_IDS ]; then
    echo "There appear to be multiple DB upgrades competing for the same array index"
    echo "This is likely the result of a failed upstream merge"
    echo "Take a look at database/upgrades/README.md and increase the numbers recently brought in"
    exit 1
fi
