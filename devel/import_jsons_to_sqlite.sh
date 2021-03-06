#!/bin/bash
if [ -z "$GRAFANA" ]
then
  echo "$0: you need to set GRAFANA env variable (Grafana suffix). For example k8s, all, prometheus etc."
  exit 1
fi
if [ -z "$1" ]
then
  echo "$0: you need to provide at least one json to import"
  exit 2
fi
./grafana/copy_grafana_dbs.sh || exit 3
cp /var/lib/grafana.$GRAFANA/grafana.db ./grafana.$GRAFANA.db || exit 4
GHA2DB_UIDMODE=1 ./sqlitedb ./grafana.$GRAFANA.db $* || exit 5
./devel/grafana_stop.sh $GRAFANA || exit 6
cp ./grafana.$GRAFANA.db /var/lib/grafana.$GRAFANA/grafana.db || exit 7
ls -l ./grafana.$GRAFANA.db.*
./devel/grafana_start.sh $GRAFANA || exit 8
echo "OK, if all is fine delete grafana.$GRAFANA.db.* db backup files and *.was json backup files".
