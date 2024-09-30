

build:
	docker build -t sublet -f ./images/sublet/Dockerfile .

up:
	kind create cluster --config ./kind.yaml

	kind load docker-image sublet

	kind get kubeconfig --internal | sed 's/kind-control-plane:6443/kubernetes.default.svc:443/g' > kubeconfig

	kubectl create configmap subcluster-source-kubeconfig --from-file=./kubeconfig

	rm kubeconfig

	kubectl apply -f ./deployment
	kubectl rollout status deploy/subcluster --timeout=90s

down:
	kind delete cluster

e2e:
	kubectl --kubeconfig ./kubeconfig.yaml create deployment nginx --image=docker.io/library/nginx:alpine --replicas=1
	kubectl --kubeconfig ./kubeconfig.yaml rollout status deploy/nginx --timeout=90s
	kubectl --kubeconfig ./kubeconfig.yaml get pod,node
	kubectl --kubeconfig ./kubeconfig.yaml expose deployment nginx --port=80
	kubectl --kubeconfig ./kubeconfig.yaml get svc
