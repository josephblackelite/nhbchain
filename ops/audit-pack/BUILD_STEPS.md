# Deterministic Build & Test Steps

These steps produce reproducible binaries for the commit pinned in `FROZEN_COMMIT.txt`.

1. **Clone the repository**
   ```bash
   git clone https://github.com/nhbchain/nhbchain.git
   cd nhbchain
   git checkout ef150bd1bbab00d426c623c06421ea0c67be03de
   ```
2. **Verify commit integrity**
   ```bash
   git show --stat
   gpg --import docs/security/repository-pgp-key.asc
   git tag -v v$(cat VERSION 2>/dev/null || echo "unknown") || echo "No signed tag for this snapshot"
   ```
3. **Set up deterministic build container**
   ```bash
   docker build -f docker/Dockerfile -t nhbchain/audit-build:ef150bd1 .
   docker run --rm -it -v "$(pwd)":/src nhbchain/audit-build:ef150bd1 bash
   ```
4. **Install Nix environment (inside container)**
   ```bash
   curl -L https://nixos.org/nix/install | sh
   . "$HOME"/.nix-profile/etc/profile.d/nix.sh
   nix-shell --pure --command "make clean build"
   ```
5. **Run deterministic tests**
   ```bash
   nix-shell --pure --command "make test"
   ```
6. **Export build artifacts**
   ```bash
   mkdir -p /src/ops/audit-pack/artifacts
   cp build/output/* /src/ops/audit-pack/artifacts/
   sha256sum /src/ops/audit-pack/artifacts/* > /src/ops/audit-pack/artifacts/SHA256SUMS
   ```

Document any deviations from these steps in your final report so we can update future audit packs.

