#!/bin/sh

UPGRADES=`grep -ho 'upgrades.[0-9][0-9]*.' database/upgrades/* | sort | wc -l`
UPGRADES_IDS=`grep -ho 'upgrades.[0-9][0-9]*.' database/upgrades/* | sort | uniq | wc -l`

if [ $UPGRADES -ne $UPGRADES_IDS ]; then
    echo "There appear to be multiple DB upgrades competing for the same array index"
    echo "This is likely the result of a failed upstream merge"
    echo "Take a look at database/upgrades/README.md and increase the numbers recently brought in"
    exit 1
fi

DECLARED_NUMBER_OF_UPGRADES=`grep -o 'const NumberOfUpgrades = [0-9]*' database/upgrades/upgrades.go | grep -o '[0-9]*'`
DESIRED_BIGGEST_NUMBER=$(expr $DECLARED_NUMBER_OF_UPGRADES - 1)
grep "upgrades.$DESIRED_BIGGEST_NUMBER." database/upgrades/* > /dev/null
if [ $? -eq 1 ]; then
    echo "Expected to have a DB upgrade number $DESIRED_BIGGEST_NUMBER (for version $DECLARED_NUMBER_OF_UPGRADES) but didn't find one"
    exit 1
fi
