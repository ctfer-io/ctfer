#!/bin/bash

# Allow insecure registries on your docker {"insecure-registries":["host.minikube.internal:5000"]}
sudo vim /etc/docker/daemon.json
# Restart docker 
sudo systemctl restart docker
# start your local docker registry 
docker run -d -p 5000:5000 --restart always --name registry registry:2
# start minikube with insecure registry
minikube start --insecure-registry="host.minikube.internal:5000"


images="ctfd/ctfd:3.5.2 redis:7.0.10 mariadb:10.7.8 traefik:v2.10.1"

for image in $images; do
    docker pull $image
    docker tag $image host.minikube.internal:5000/$image
    docker push host.minikube.internal:5000/$image
    docker rmi $image
    docker rmi host.minikube.internal:5000/$image
done



