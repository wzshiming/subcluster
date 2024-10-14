
KIND=go run sigs.k8s.io/kind@v0.24.0
HYDROPHONE=go run sigs.k8s.io/hydrophone@v0.6.0

build:
	go mod vendor
	docker build -t sublet -f ./images/sublet/Dockerfile .

load-image:
	$(KIND) load docker-image sublet

#	$(KIND) load docker-image docker.io/library/nginx:alpine
#	$(KIND) load docker-image ghcr.io/wzshiming/kwok/cluster:v0.7.0-alpha.2-k8s.v1.31.0
#	$(KIND) load docker-image registry.k8s.io/conformance:v1.31.0
#	$(KIND) load docker-image registry.k8s.io/e2e-test-images/busybox:1.36.1-1

create-cluster:
	$(KIND) create cluster --config ./kind.yaml

delete-cluster:
	$(KIND) delete cluster

install:
	$(KIND) get kubeconfig --internal | sed 's/kind-control-plane:6443/kubernetes.default.svc:443/g' > kubeconfig

	kubectl create configmap subcluster-source-kubeconfig --from-file=./kubeconfig

	kubectl create -f ./deployment
	kubectl rollout status deploy/subcluster --timeout=90s

uninstall:
	kubectl delete -f ./deployment
	kubectl delete configmap subcluster-source-kubeconfig

up: build create-cluster load-image install

down: delete-cluster

e2e:
	kubectl --kubeconfig ./kubeconfig.yaml create deployment nginx --image=docker.io/library/nginx:alpine --replicas=1
	kubectl --kubeconfig ./kubeconfig.yaml rollout status deploy/nginx --timeout=90s
	kubectl --kubeconfig ./kubeconfig.yaml get pod,node
	kubectl --kubeconfig ./kubeconfig.yaml expose deployment nginx --port=80
	kubectl --kubeconfig ./kubeconfig.yaml get svc

conformance:
	$(HYDROPHONE) --conformance --kubeconfig ./kubeconfig.yaml

conformance-cleanup:
	$(HYDROPHONE) --cleanup --kubeconfig ./kubeconfig.yaml

local:
	$(KIND) get kubeconfig > ./source-kubeconfig
	kwokctl create cluster \
		--runtime=binary \
		--disable=kwok-controller
	kwokctl get kubeconfig > ./kubeconfig.yaml
	go run ./cmd/sublet \
		--kubeconfig=./kubeconfig.yaml \
		--name=sublet  \
		--node-name=subnode \
		--node-ip=127.0.0.1 \
		--node-port=7878 \
		--source-kubeconfig=./source-kubeconfig \
		--source-node-name=kind-control-plane \
		--source-namespace=default
