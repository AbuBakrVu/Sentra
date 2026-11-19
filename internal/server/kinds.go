package server

import "strings"

// Устройства, которые приходят не от агента, а из API системы виртуализации:
// Proxmox (pve-node, pve-qemu, pve-lxc) и VMware (vmw-host, vmw-vm).
// Правила у них общие: узел/хост и машины на нём, статус берём из API.

const (
	kindVMWHost = "vmw-host"
	kindVMWVM   = "vmw-vm"
)

// isVirt — устройство из Proxmox или VMware.
func isVirt(kind string) bool {
	return strings.HasPrefix(kind, "pve-") || strings.HasPrefix(kind, "vmw-")
}

// isVirtNode — узел Proxmox или хост ESXi: на нём стоят машины.
func isVirtNode(kind string) bool { return kind == "pve-node" || kind == kindVMWHost }

// isVirtGuest — виртуальная машина или контейнер.
func isVirtGuest(kind string) bool {
	return kind == "pve-qemu" || kind == "pve-lxc" || kind == kindVMWVM
}

// virtName — название системы для текстов: «Нет данных от Proxmox».
func virtName(kind string) string {
	if strings.HasPrefix(kind, "vmw-") {
		return "VMware"
	}
	return "Proxmox"
}

// nodeDownTitle — узел или хост не на связи с кластером (vCenter).
func nodeDownTitle(kind string) string {
	if kind == kindVMWHost {
		return "Хост ESXi не на связи с vCenter"
	}
	return "Узел не в сети кластера"
}

// guestNodeDownTitle — у машины недоступен узел или хост, на котором она стоит.
func guestNodeDownTitle(kind string) string {
	if kind == kindVMWVM {
		return "Хост ESXi машины не на связи"
	}
	return "Узел Proxmox не в сети"
}
