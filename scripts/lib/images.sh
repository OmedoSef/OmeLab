# shellcheck shell=bash disable=SC2034  # tableaux utilisés par les scripts qui sourcent ce fichier
# Catalogue des images cloud (cloud-init) utilisables par image-get.sh / vm-create.sh.
# Ajouter une entrée = une URL + un identifiant osinfo (virt-install --osinfo list).
declare -A IMAGE_URL=(
  [debian12]="https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2"
  [debian13]="https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.qcow2"
  [ubuntu2404]="https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
  [ubuntu2604]="https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img"
  [rocky9]="https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2"
  [alma9]="https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2"
)
declare -A IMAGE_OSINFO=(
  [debian12]=debian12
  [debian13]=debian13
  [ubuntu2404]=ubuntu24.04
  [ubuntu2604]=ubuntu24.04
  [rocky9]=rocky9
  [alma9]=almalinux9
)

image_path() { echo "$LABS_IMAGES_DIR/$1.qcow2"; }
