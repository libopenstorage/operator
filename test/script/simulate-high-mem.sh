set -o xtrace

podCount=100
configMapSize=500K
configMapCount=3
configMapFile=data.tmp
namespace=kube-test

# create a file with configMapSize
dd if=/dev/zero of=$configMapFile  bs=$configMapSize  count=1

kubectl create namespace $namespace

i=1
while [ $i -le $configMapCount ]
do
  kubectl -n $namespace create configmap cm$i --from-file=$configMapFile
  i=$(( $i + 1 ))
done

rm $configMapFile

kubectl -n $namespace apply -f testapp.yaml
kubectl -n $namespace scale deployment testapp --replicas=$podCount
