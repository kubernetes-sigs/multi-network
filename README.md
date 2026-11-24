# Multi-Network

This `multi-network` repository contains the controller implementation and testing framework for the **Kubernetes Multi-Network** sub-project under [SIG Network](https://github.com/kubernetes/community/tree/master/sig-network).

This repository provides:
- **`multi-network-controller`:** Reconciles `PodNetworkKind` resources, verifies that underlying implementation resource types and RBAC permissions are available, manages readiness status conditions, and enforces deletion protection.
- **Conformance Tests:** Tests to validate that pod network implementations correctly integrate with Kubernetes Dynamic Resource Allocation (DRA) and adhere to the Multi-Network specification.

For the API definitions and specifications, see [kubernetes-sigs/multi-network-api](https://github.com/kubernetes-sigs/multi-network-api).

For more details, see:
- [Requirements and Use Cases](https://github.com/kubernetes-sigs/multi-network-api/blob/main/docs/requirements.md)
- [PodNetworkKind Design Specification](https://github.com/kubernetes-sigs/multi-network-api/blob/main/docs/pod-network-kind.md)

## Getting Started

```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/multi-network/refs/heads/main/deployment/multi-network-controller.yaml
```

To install the `PodNetworkKind` CRD, visit https://github.com/kubernetes-sigs/multi-network-api.

Go package documentation:
- https://pkg.go.dev/sigs.k8s.io/multi-network-api
- https://pkg.go.dev/sigs.k8s.io/multi-network

## Community, Discussion, and Support

This project is maintained by Kubernetes [SIG Network](https://github.com/kubernetes/community/tree/master/sig-network) under the Multi-Network sub-project.

- **API Specification:** [kubernetes-sigs/multi-network-api](https://github.com/kubernetes-sigs/multi-network-api)
- **Meeting Notes:** [Multi-Network Meeting Notes](https://docs.google.com/document/d/1pe_0aOsI35BEsQJ-FhFH9Z_pWQcU2uqwAnOx2NIx6OY/edit?usp=sharing)
- **Bi-weekly Meeting (Tuesdays at 06:00 PT (Pacific Time)):** [Zoom Link](https://zoom.us/j/95680858961?pwd=M1c2TTdMZHpMUUtIYXRpbjRobkNJZz09)
- **Meeting Recordings:** [YouTube Kubernetes Community](https://www.youtube.com/@KubernetesCommunity/search?query=multi-network)
- **Slack:** [#sig-network-multi-network](https://kubernetes.slack.com/archives/C03UT5H9KDZ) on [Kubernetes Slack](https://slack.k8s.io/)
- **Mailing List:** [SIG Network Google Group](https://groups.google.com/a/kubernetes.io/g/sig-network)

### Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for instructions on how to contribute to this repository.

### Code of Conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

### License

This project is licensed under the Apache 2.0 License - see the [LICENSE](LICENSE) file for details.