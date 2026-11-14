"""Validate explicit macOS shares and prepare a non-login file-sharing identity."""

import grp
import os
from pathlib import Path
import pwd
import re
import subprocess

from sandbox_mounts import canonical_source, validated_readonly, validate_mount_shape, validate_mount_set
from sandbox_share_identity import require
from sandbox_share_install import install_commands


def validate_mounts(manifest):
    if not isinstance(manifest, list) or not manifest:
        raise ValueError("Select at least one directory in the mount list")
    mounts = [validate_mount(entry) for entry in manifest]
    validate_mount_set(mounts)
    return mounts


def validate_mount(entry):
    name = validate_mount_shape(entry)
    source = canonical_source(entry.get("host_path"))
    if not re.fullmatch(r"/Users/[^/]+/[^/]+/.+", source):
        raise ValueError("Mac sharing requires selected project directories below /Users/USER/DIRECTORY/")
    if entry.get("container_mount_path") != source:
        raise ValueError(
            f"Project {name!r}: container_mount_path is {entry.get('container_mount_path')!r}. "
            f"Set container_mount_path to {source!r}. "
            "Mac sharing requires the same absolute path on host and container "
            "so existing absolute symlinks resolve."
        )
    return dict(project_name=name, host_path=source, container_mount_path=source,
                container_readonly=validated_readonly(entry.get("container_readonly", True)))


def validate_username(user):
    if not re.fullmatch(r"collab-share(?:-[a-z0-9-]{1,16})?", user):
        raise ValueError("Use collab-share or collab-share-NAME for the dedicated account")


def identity(user):
    validate_username(user)
    account = pwd.getpwnam(user)
    validate_login(account)
    problem = primary_group_problem(account)
    if problem:
        raise ValueError(problem.message())
    validate_groups(account, grp.getgrgid(account.pw_gid))
    validate_unique(account)
    return account


def validate_login(account):
    require(min(account.pw_uid, account.pw_gid) > 0 and account.pw_uid != os.getuid(),
            "Sharing cannot use root or the operator identity")
    require((account.pw_shell, account.pw_dir) == ("/usr/bin/false", "/var/empty"),
            "Sharing account must have /usr/bin/false shell and /var/empty home")


def validate_groups(account, group):
    user = account.pw_name
    require(group.gr_name == user and set(group.gr_mem).issubset({user}),
            "Sharing requires a private same-name primary group")
    # macOS adds implicit groups even to daemon users. The installed helper
    # clears them all before serving; reject administrative membership outright.
    groups = set(os.getgrouplist(user, account.pw_gid))
    if groups.intersection({0, 20, 80}):
        raise ValueError("Sharing account belongs to wheel, staff or admin")


class PrimaryGroupProblem:
    """The account's primary group isn't its same-name group (#174; a macOS
    update has reset it to staff). It names both groups and, only when
    nothing else uses the same-name group or the IDs, the operator's repair
    command. Nothing here changes the account."""

    def __init__(self, account, expected, blockers):
        self.account, self.expected, self.blockers = account, expected, blockers

    def repair_command(self):
        """dscl -change fails if the primary group changed again meanwhile."""
        user, gid = self.account.pw_name, self.account.pw_gid
        return f"sudo dscl . -change /Users/{user} PrimaryGroupID {gid} {self.expected.gr_gid}"

    def message(self):
        user = self.account.pw_name
        expected = (f"{user} (GID {self.expected.gr_gid})" if self.expected
                    else f"{user}, but /Groups/{user} doesn't exist")
        found = (f"Sharing account {user} (UID {self.account.pw_uid}) has primary group "
                 f"{group_label(self.account.pw_gid)}; it must be its private group {expected}.")
        if self.blockers:
            return (f"{found} No repair is suggested: {'; '.join(self.blockers)}. Inspect with: "
                    f"dscl . -read /Users/{user} and dscl . -read /Groups/{user}.")
        return (f"{found} A macOS update can reset it. Repair it with: {self.repair_command()}, then rerun "
                "pairtask share setup --mounts-file FILE to reapply names and ACLs.")


def primary_group_problem(account):
    """None when the primary group is the account's same-name group."""
    expected = same_name_group(account.pw_name)
    if expected is not None and expected.gr_gid == account.pw_gid:
        return None
    return PrimaryGroupProblem(account, expected, repair_blockers(account, expected))


def same_name_group(user):
    try:
        return grp.getgrnam(user)
    except KeyError:
        return None


def group_label(gid):
    try:
        return f"{grp.getgrgid(gid).gr_name} (GID {gid})"
    except KeyError:
        return f"GID {gid}, which no group has"


def repair_blockers(account, expected):
    """Why pointing the account at its same-name group would not be safe."""
    user = account.pw_name
    if expected is None:
        return [f"/Groups/{user} doesn't exist"]
    blockers = [f"/Groups/{user} has other members: {', '.join(sorted(set(expected.gr_mem) - {user}))}"
                ] if set(expected.gr_mem) - {user} else []
    if expected.gr_gid <= 0:
        blockers.append(f"/Groups/{user} has GID {expected.gr_gid}")
    blockers += [f"GID {expected.gr_gid} is also the group {other.gr_name}"
                 for other in grp.getgrall() if other.gr_gid == expected.gr_gid and other.gr_name != user]
    return blockers + id_conflicts(account, expected.gr_gid)


def id_conflicts(account, gid):
    conflicts = []
    for other in pwd.getpwall():
        if other.pw_name == account.pw_name:
            continue
        if other.pw_gid == gid:
            conflicts.append(f"GID {gid} is also the primary group of {other.pw_name}")
        if other.pw_uid == account.pw_uid:
            conflicts.append(f"UID {account.pw_uid} is also used by {other.pw_name}")
    return conflicts


def validate_unique(account):
    for other in pwd.getpwall():
        if other.pw_name == account.pw_name:
            continue
        if other.pw_uid == account.pw_uid or other.pw_gid == account.pw_gid:
            raise ValueError("Sharing UID/GID is also used by another account")


def unused_account(user, uid):
    accounts, records = pwd.getpwall(), grp.getgrall()
    check_orphaned_group(user, accounts, records)
    users = {value for account in accounts for value in (account.pw_name, account.pw_uid)}
    groups = {value for group in records for value in (group.gr_name, group.gr_gid)}
    if {user, uid}.intersection(users | groups):
        raise ValueError(f"Sharing account or ID {uid} already exists; inspect it before setup")


def check_orphaned_group(user, accounts, groups):
    if any(account.pw_name == user for account in accounts):
        return
    for group in groups:
        if group.gr_name == user:
            raise ValueError(
                f"Sharing group /Groups/{user} (GID {group.gr_gid}) exists but /Users/{user} is missing. "
                f"Inspect it with: dscl . -read /Groups/{user}. "
                "Have the operator restore the account or remove the confirmed orphaned group, "
                "then rerun sandbox-share.py setup to reapply project ACLs. "
                "No existing group will be adopted or deleted automatically."
            )


def account_commands(user):
    validate_username(user)
    try:
        identity(user)
        # Repair older valid accounts too. Setting the same display names again
        # is idempotent and does not change their UID, groups or restrictions.
        return realname_commands(user)
    except KeyError:
        pass
    uid = 60000
    unused_account(user, uid)
    return create_account_commands(user, uid)


def create_account_commands(user, uid):
    group, account = f"/Groups/{user}", f"/Users/{user}"
    entries = [(group, "RealName", "collab-ai share"), (group, "PrimaryGroupID", str(uid)),
               (account, "RealName", "collab-ai share"), (account, "UniqueID", str(uid)),
               (account, "PrimaryGroupID", str(uid)), (account, "UserShell", "/usr/bin/false"),
               (account, "NFSHomeDirectory", "/var/empty"), (account, "Password", "*"),
               (account, "IsHidden", "1")]
    return [["sudo", "-n", "/usr/bin/dscl", ".", "-create", path, key, value]
            for path, key, value in entries]


def realname_commands(user):
    return [["sudo", "-n", "/usr/bin/dscl", ".", "-create", f"/{kind}/{user}", "RealName", "collab-ai share"]
            for kind in ("Groups", "Users")]


def acl_commands(mounts, user):
    operator = pwd.getpwuid(os.getuid()).pw_name
    commands = []
    parents = set()
    for entry in mounts:
        source = Path(entry["host_path"])
        parents.update(parent for parent in source.parents if str(parent).startswith("/Users/"))
        rights = access_rights(entry["container_readonly"])
        rule = f"user:{user} allow {rights},file_inherit,directory_inherit"
        commands.append(owned_tree_command(source, operator, rule))
        if not entry["container_readonly"]:
            rule = f"user:{operator} allow {rights},file_inherit,directory_inherit"
            commands.append(owned_tree_command(source, operator, rule))
    for parent in sorted(parents):
        commands.insert(0, ["/bin/chmod", "+a", f"user:{user} allow search", str(parent)])
    return commands


def owned_tree_command(source, operator, rule):
    """Add rule to every file and directory under source that the operator owns.
    Only an owner may change an ACL. Entries the share account created through
    a running share are its own, and already carry both rules by inheritance,
    so they are skipped rather than failing a later setup. Symbolic links are
    never followed or changed."""
    return ["/usr/bin/find", str(source), "-user", operator, "(", "-type", "f", "-o", "-type", "d", ")",
            "-exec", "/bin/chmod", "+a", rule, "{}", "+"]


def access_rights(readonly):
    rights = "read,list,search,readattr,readextattr"
    if not readonly:
        rights += ",write,append,add_file,add_subdirectory,delete,delete_child,writeattr,writeextattr"
    return rights


def setup(mounts, user):
    commands = account_commands(user) + install_commands()
    subprocess.run(["sudo", "-v"], check=True)
    for command in commands:
        subprocess.run(command, check=True)
    identity(user)
    # ACL changes run as the operator, never as root, and only on the operator's own entries.
    for command in acl_commands(mounts, user):
        subprocess.run(command, check=True)
