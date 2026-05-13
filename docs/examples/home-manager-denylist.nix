# dotkeeper - denylist-first Home Manager sketch
#
# This is a public, generic pattern. Keep your real host names, device IDs, repo
# paths, and per-repo choices in your private flake or dotfiles repo.
#
# The idea:
#
#   1. Declare scan roots.
#   2. Declare the few paths that must not sync.
#   3. During Home Manager activation, discover Git repos under scan roots.
#   4. Write local .dotkeeper.toml files only for repos that are not denied.
#   5. If a denied path is inside a managed repo, write it as a per-repo
#      Syncthing ignore pattern instead of dropping the whole repo.

{ lib, pkgs, ... }:

let
  git = lib.getExe pkgs.git;

  scanRoots = [
    "~/Documents/GitHub"
    "~/.config"
  ];

  localOnlyPatterns = [
    ".git"
    ".dotkeeper.toml"
    "dotkeeper.toml"
    ".stignore"
    ".dkfolder"
    ".stfolder"
    "*.sync-conflict-*"
    ".syncthing.*.tmp"
  ];

  gitExcludePatterns = [
    ".dotkeeper.toml"
    "dotkeeper.toml"
    ".stignore"
    ".dkfolder"
    ".stfolder"
    "*.sync-conflict-*"
    ".syncthing.*.tmp"
  ];

  dontSync = {
    all = [
      # Public source checkout: Git is the transport for dotkeeper itself.
      "~/Documents/GitHub/dotkeeper"

      # Archive material is not part of the live sync mesh.
      "~/Documents/GitHub/archive"

      # Repo-local generated data: keep the repo synced, ignore this subtree.
      "~/Documents/GitHub/example-rag/workspace"
    ];

    laptop = [
      # Heavy workspaces can stay on the desktop only.
      "~/Documents/GitHub/video-renderer"
    ];

    desktop = [];
  };

  # In a real flake, pass this from the host module instead of hard-coding it.
  hostName = "laptop";
  allDenied = dontSync.all ++ (dontSync.${hostName} or []);
in
{
  home.activation.dotkeeperRepos = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
    set -eu

    expand_path() {
      case "$1" in
        "~") printf '%s\n' "$HOME" ;;
        "~/"*) printf '%s/%s\n' "$HOME" "''${1#\~/}" ;;
        *) printf '%s\n' "$1" ;;
      esac
    }

    is_denied_repo() {
      repo_path="$1"
      while IFS= read -r denied_path; do
        if [ "$repo_path" = "$denied_path" ] || [ "''${repo_path#"$denied_path"/}" != "$repo_path" ]; then
          return 0
        fi
      done < "$denied_file"
      return 1
    }

    repo_ignore_patterns() {
      repo_path="$1"
      while IFS= read -r denied_path; do
        if [ "''${denied_path#"$repo_path"/}" != "$denied_path" ]; then
          rel="''${denied_path#"$repo_path"/}"
          [ -n "$rel" ] && printf '%s\n' "$rel"
        fi
      done < "$denied_file" | sort -u
    }

    write_local_ignores() {
      repo_path="$1"

      {
        ${lib.concatStringsSep "\n        " (map (pattern: "printf '%s\\n' ${lib.escapeShellArg pattern}") localOnlyPatterns)}
        repo_ignore_patterns "$repo_path"
      } > "$repo_path/.stignore"

      exclude_path="$(${git} -C "$repo_path" rev-parse --git-path info/exclude)"
      mkdir -p "$(dirname "$exclude_path")"
      touch "$exclude_path"

      append_exclude() {
        pattern="$1"
        if ! grep -Fxq "$pattern" "$exclude_path"; then
          printf '%s\n' "$pattern" >> "$exclude_path"
        fi
      }

      ${lib.concatStringsSep "\n      " (map (pattern: "append_exclude ${lib.escapeShellArg pattern}") gitExcludePatterns)}
    }

    state_dir="''${XDG_STATE_HOME:-$HOME/.local/state}/dotkeeper"
    mkdir -p "$state_dir"
    denied_file="$state_dir/dont-sync.paths"
    : > "$denied_file"

    ${lib.concatStringsSep "\n    " (map (path: "printf '%s\\n' \"$(expand_path ${lib.escapeShellArg path})\" >> \"$denied_file\"") allDenied)}

    for root in ${lib.concatStringsSep " " (map lib.escapeShellArg scanRoots)}; do
      root_path="$(expand_path "$root")"
      [ -d "$root_path" ] || continue

      find "$root_path" -maxdepth 4 -name .git -print 2>/dev/null | while IFS= read -r git_dir; do
        repo_path="$(dirname "$git_dir")"
        ${git} -C "$repo_path" rev-parse --is-inside-work-tree >/dev/null 2>&1 || continue

        if is_denied_repo "$repo_path"; then
          rm -f "$repo_path/.dotkeeper.toml"
          continue
        fi

        repo_name="$(basename "$repo_path")"
        folder_hash="$(printf '%s' "$repo_name" | sha256sum | cut -c1-6)"
        folder_id="dk-$repo_name-$folder_hash"

        {
          printf '%s\n' 'schema_version = 2'
          printf '%s\n\n' '[repo]'
          printf 'name = "%s"\n' "$repo_name"
          printf 'added = "2026-01-01T00:00:00Z"\n'
          printf 'added_by = "home-manager"\n\n'
          printf '%s\n' '[sync]'
          printf 'syncthing_folder_id = "%s"\n' "$folder_id"
          printf '%s\n' 'ignore = ['
          repo_ignore_patterns "$repo_path" | while IFS= read -r pattern; do
            printf '  "%s",\n' "$pattern"
          done
          printf '%s\n' ']'
          printf '%s\n' 'share_with = []'
        } > "$repo_path/.dotkeeper.toml"

        write_local_ignores "$repo_path"
      done
    done
  '';
}
