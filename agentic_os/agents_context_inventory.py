"""Inventory AGENTS context without treating every repository as eager input."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable


REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_SUBSTRATE_MANIFEST = (
    REPO_ROOT / "aos-cli" / "repositories" / "substrate-repos.txt"
)
DEFAULT_PROJECTS_ROOT = Path.home() / "projects"
DEFAULT_AOSH_REPO = "coilyco-bridge/agentic-os-hardware"
GLOBAL_BASE_REPO = "coilyco-flight-deck/agentic-os"
FORMAT = "agentic-os.agents-context-inventory.v1"
TOKENIZER = "chars/4 proxy"
VISIBILITIES = {"public", "private", "unknown"}
SKIP_DIRS = {
    ".git",
    ".mypy_cache",
    ".pytest_cache",
    ".ruff_cache",
    ".terraform",
    ".tox",
    ".venv",
    "__pycache__",
    "build",
    "dist",
    "node_modules",
    "target",
    "vendor",
}
HEADING_RE = re.compile(r"^#{1,6}\s+(.+?)\s*$")
HARNESS_OVERRIDE_RE = re.compile(r"^AGENTS\.([a-z0-9-]+)\.md$")
ROLE_TERMS = {
    "advocate",
    "science",
    "frontend",
    "gamedev",
    "platform",
    "sysadmin",
    "director",
    "analyst",
}
TASK_TERMS = {
    "ci/cd",
    "deploy",
    "deployment",
    "incident",
    "release",
    "rollout",
    "workflow",
}
DOC_HEADINGS = {
    "layout",
    "project shape",
    "references",
    "related",
    "see also",
}


class InventoryError(RuntimeError):
    """A manifest, repository, or board violates the inventory contract."""


@dataclass(frozen=True)
class ManifestEntry:
    ref: str
    visibility: str


@dataclass
class ParagraphRecord:
    id: str
    hash: str
    classification: str
    destination: str
    basis: str
    duplicate_of: str | None = None

    def as_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "hash": self.hash,
            "classification": self.classification,
            "destination": self.destination,
            "basis": self.basis,
            "duplicate_of": self.duplicate_of,
        }


@dataclass
class DocumentRecord:
    repo: str
    path: str
    kind: str
    audience: tuple[str, ...]
    load_condition: str
    bytes: int
    tokens: int
    lines: int
    content_hash: str
    paragraphs: list[ParagraphRecord] = field(default_factory=list)

    @property
    def id(self) -> str:
        return f"{self.repo}:{self.path}"

    def as_dict(self) -> dict[str, Any]:
        return {
            "id": self.id,
            "path": self.path,
            "kind": self.kind,
            "audience": list(self.audience),
            "load_condition": self.load_condition,
            "bytes": self.bytes,
            "tokens": self.tokens,
            "lines": self.lines,
            "content_hash": self.content_hash,
            "paragraphs": [paragraph.as_dict() for paragraph in self.paragraphs],
        }


@dataclass
class RepositoryRecord:
    full_name: str
    name: str
    kind: str
    visibility: str
    present: bool
    root_agents: str
    documents: list[DocumentRecord] = field(default_factory=list)

    def as_dict(self) -> dict[str, Any]:
        return {
            "full_name": self.full_name,
            "name": self.name,
            "kind": self.kind,
            "visibility": self.visibility,
            "present": self.present,
            "root_agents": self.root_agents,
            "global_load": self.full_name == GLOBAL_BASE_REPO,
            "documents": [document.as_dict() for document in self.documents],
        }


@dataclass(frozen=True)
class ContextSelection:
    role: str
    harness: str


def estimate_tokens(text: str) -> int:
    """Return the repository's deterministic chars/4 proxy."""
    return -(-len(text) // 4)


def _valid_repo_ref(value: str) -> bool:
    parts = value.split("/")
    return len(parts) in {1, 2} and all(
        part and part not in {".", ".."} and re.fullmatch(r"[A-Za-z0-9._-]+", part)
        for part in parts
    )


def load_manifest(
    path: Path, *, default_visibility: str = "unknown"
) -> tuple[ManifestEntry, ...]:
    """Read `owner/name [visibility]` entries with comments and blanks ignored."""
    if default_visibility not in VISIBILITIES:
        raise InventoryError(f"invalid default visibility: {default_visibility}")
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise InventoryError(f"read {path}: {exc}") from exc
    entries: dict[str, ManifestEntry] = {}
    for number, raw in enumerate(lines, 1):
        line = raw.split("#", 1)[0].strip()
        if not line:
            continue
        columns = line.split()
        if len(columns) not in {1, 2}:
            raise InventoryError(f"{path}:{number}: expected repo and optional visibility")
        ref = columns[0]
        visibility = columns[1] if len(columns) == 2 else default_visibility
        if not _valid_repo_ref(ref):
            raise InventoryError(f"{path}:{number}: invalid repository ref {ref!r}")
        if visibility not in VISIBILITIES:
            raise InventoryError(
                f"{path}:{number}: visibility must be public, private, or unknown"
            )
        previous = entries.get(ref)
        if previous is not None and previous.visibility != visibility:
            raise InventoryError(f"{path}:{number}: conflicting visibility for {ref}")
        entries[ref] = ManifestEntry(ref=ref, visibility=visibility)
    if not entries:
        raise InventoryError(f"{path}: manifest has no repositories")
    return tuple(entries[key] for key in sorted(entries))


def _resolve_entry(entry: ManifestEntry, projects_root: Path) -> tuple[str, Path]:
    if "/" in entry.ref:
        return entry.ref, projects_root / entry.ref
    matches = sorted(
        path
        for path in projects_root.glob(f"*/{entry.ref}")
        if path.is_dir() or path.is_symlink()
    )
    if len(matches) > 1:
        owners = ", ".join(path.parent.name for path in matches)
        raise InventoryError(f"bare repo {entry.ref!r} is ambiguous across: {owners}")
    if matches:
        path = matches[0]
        return f"{path.parent.name}/{path.name}", path
    return entry.ref, projects_root / entry.ref


def _agent_file_kind(path: Path) -> tuple[str, tuple[str, ...], str] | None:
    name = path.name
    if name == "AGENTS.md":
        return "agents-base", ("all",), "repo-cascade"
    if name == "AGENTS.COMPOSE.md":
        return "composed-source", ("all",), "global-compose-source"
    if name == "CLAUDE.md":
        return "load-point-bridge", ("claude",), "repo-cascade-bridge"
    match = HARNESS_OVERRIDE_RE.fullmatch(name)
    if match is not None:
        harness = match.group(1)
        if harness != "compose":
            return "harness-override", (harness,), "compose-time-override"
    return None


def _discover_agent_files(repo_path: Path) -> list[Path]:
    found: list[Path] = []
    for path in repo_path.rglob("*.md"):
        try:
            relative = path.relative_to(repo_path)
        except ValueError:
            continue
        if any(part in SKIP_DIRS for part in relative.parts[:-1]):
            continue
        if _agent_file_kind(path) is not None and (path.is_file() or path.is_symlink()):
            found.append(path)
    return sorted(found, key=lambda path: path.relative_to(repo_path).as_posix())


def _read_document(path: Path) -> str:
    if path.is_symlink():
        try:
            return f"symlink -> {path.readlink()}\n"
        except OSError as exc:
            raise InventoryError(f"read symlink {path}: {exc}") from exc
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        raise InventoryError(f"read {path}: {exc}") from exc


def _paragraphs(text: str) -> list[tuple[str, str]]:
    result: list[tuple[str, str]] = []
    heading = ""
    buffer: list[str] = []

    def flush() -> None:
        if not buffer:
            return
        normalized = " ".join(" ".join(buffer).split())
        if normalized:
            result.append((heading, normalized))
        buffer.clear()

    for line in text.splitlines():
        match = HEADING_RE.fullmatch(line)
        if match is not None:
            flush()
            heading = match.group(1).strip()
        elif line.strip():
            buffer.append(line.strip())
        else:
            flush()
    flush()
    return result


def _classification(
    repo: RepositoryRecord,
    document: DocumentRecord,
    heading: str,
    text: str,
) -> tuple[str, str, str]:
    scope = f"{heading} {text}".lower()
    heading_lower = heading.lower()
    if document.kind == "load-point-bridge" or (
        "generated" in scope and "do not edit" in scope
    ):
        return "generated-pointer", "validator/code", "generated delivery artifact"
    if repo.full_name == GLOBAL_BASE_REPO and document.path == "AGENTS.md":
        return "universal", "global person context", "canonical composed global base"
    if any(re.search(rf"\b{re.escape(term)}\b", scope) for term in ROLE_TERMS):
        return "role-specific", "role COMPOSED.md", "role vocabulary"
    if any(term in scope for term in TASK_TERMS):
        return "task-specific", "ordinary skill", "task or lifecycle vocabulary"
    if heading_lower in DOC_HEADINGS:
        return "documentation-only", "repo docs", "navigation or explanatory section"
    return (
        "repo-specific-unconditional",
        "repo AGENTS.md",
        "no narrower deterministic signal",
    )


def _document_record(
    repo: RepositoryRecord, repo_path: Path, path: Path
) -> DocumentRecord:
    metadata = _agent_file_kind(path)
    if metadata is None:
        raise InventoryError(f"unsupported agent file: {path}")
    kind, audience, load_condition = metadata
    text = _read_document(path)
    encoded = text.encode("utf-8")
    relative = path.relative_to(repo_path).as_posix()
    document = DocumentRecord(
        repo=repo.full_name,
        path=relative,
        kind=kind,
        audience=audience,
        load_condition=load_condition,
        bytes=len(encoded),
        tokens=estimate_tokens(text),
        lines=len(text.splitlines()),
        content_hash=hashlib.sha256(encoded).hexdigest(),
    )
    for index, (heading, paragraph) in enumerate(_paragraphs(text), 1):
        classification, destination, basis = _classification(
            repo, document, heading, paragraph
        )
        digest = hashlib.sha256(paragraph.encode("utf-8")).hexdigest()
        document.paragraphs.append(
            ParagraphRecord(
                id=f"{document.id}#p{index}",
                hash=digest,
                classification=classification,
                destination=destination,
                basis=basis,
            )
        )
    return document


def discover_repositories(
    substrate_manifest: Path,
    fleet_manifest: Path,
    projects_root: Path,
    *,
    aosh_repo: str = DEFAULT_AOSH_REPO,
) -> list[RepositoryRecord]:
    """Resolve only manifest-named repos, then inspect their AGENTS surfaces."""
    substrate_entries = load_manifest(
        substrate_manifest, default_visibility="public"
    )
    fleet_entries = load_manifest(fleet_manifest)
    substrate_refs = {entry.ref for entry in substrate_entries}
    resolved: dict[str, tuple[ManifestEntry, Path]] = {}
    for entry in (*substrate_entries, *fleet_entries):
        full_name, path = _resolve_entry(entry, projects_root)
        previous = resolved.get(full_name)
        if previous is None:
            resolved[full_name] = (entry, path)
            continue
        previous_entry, previous_path = previous
        visibility = previous_entry.visibility
        if visibility == "unknown":
            visibility = entry.visibility
        elif entry.visibility not in {"unknown", visibility}:
            raise InventoryError(f"conflicting visibility for {full_name}")
        resolved[full_name] = (
            ManifestEntry(ref=full_name, visibility=visibility),
            previous_path,
        )

    repositories: list[RepositoryRecord] = []
    for full_name in sorted(resolved):
        entry, path = resolved[full_name]
        source_ref = full_name if "/" in full_name else entry.ref
        if full_name == aosh_repo:
            kind = "aosh"
        elif source_ref in substrate_refs or entry.ref in substrate_refs:
            kind = "substrate"
        else:
            kind = "product"
        present = path.is_dir()
        repo = RepositoryRecord(
            full_name=full_name,
            name=full_name.rsplit("/", 1)[-1],
            kind=kind,
            visibility=entry.visibility,
            present=present,
            root_agents="missing",
        )
        if present:
            for agent_file in _discover_agent_files(path):
                document = _document_record(repo, path, agent_file)
                repo.documents.append(document)
                if document.path == "AGENTS.md":
                    repo.root_agents = "present"
        repositories.append(repo)
    _mark_duplicates(repositories)
    return repositories


def _repo_priority(repo: RepositoryRecord) -> tuple[int, str]:
    if repo.full_name == GLOBAL_BASE_REPO:
        return (0, repo.full_name)
    return ({"substrate": 1, "aosh": 2, "product": 3}[repo.kind], repo.full_name)


def _mark_duplicates(repositories: list[RepositoryRecord]) -> None:
    owners: dict[str, str] = {}
    for repo in sorted(repositories, key=_repo_priority):
        for document in repo.documents:
            for paragraph in document.paragraphs:
                owner = owners.get(paragraph.hash)
                if owner is None:
                    owners[paragraph.hash] = paragraph.id
                    continue
                paragraph.duplicate_of = owner
                paragraph.classification = "duplicate"
                paragraph.destination = "deletion"
                paragraph.basis = "exact normalized paragraph hash"


def _document_map(
    repositories: Iterable[RepositoryRecord],
) -> dict[str, DocumentRecord]:
    return {
        document.id: document
        for repo in repositories
        for document in repo.documents
    }


def _active_repo_paths(cwd: str) -> tuple[str, ...]:
    relative = Path(cwd)
    if relative.is_absolute() or ".." in relative.parts:
        raise InventoryError("cwd must be relative to the selected repository")
    directories = [Path(".")]
    current = Path(".")
    for part in relative.parts:
        if part in {"", "."}:
            continue
        current /= part
        directories.append(current)
    return tuple(
        "AGENTS.md" if directory == Path(".") else (directory / "AGENTS.md").as_posix()
        for directory in directories
    )


def active_cascade(
    repositories: list[RepositoryRecord],
    selection: ContextSelection,
    *,
    current_repo: str,
    cwd: str,
    include_global_composed: bool = True,
) -> dict[str, Any]:
    """Return source references for one explicit role and harness."""
    by_repo = {repo.full_name: repo for repo in repositories}
    if current_repo not in by_repo:
        raise InventoryError(
            f"current repository {current_repo!r} is not present in the inventory"
        )
    documents = _document_map(repositories)
    sources: list[dict[str, Any]] = []

    def append(document_id: str, delivery_path: str) -> None:
        document = documents.get(document_id)
        if document is None:
            return
        sources.append(
            {
                "source": document.id,
                "delivery_path": delivery_path,
                "bytes": document.bytes,
                "tokens": document.tokens,
                "content_hash": document.content_hash,
            }
        )

    if include_global_composed:
        append(f"{GLOBAL_BASE_REPO}:AGENTS.md", "global-composed")
        append(
            f"{GLOBAL_BASE_REPO}:AGENTS.{selection.harness}.md",
            "global-harness-override",
        )
    active_paths = _active_repo_paths(cwd)
    if selection.harness == "claude":
        for agents_path in active_paths:
            parent = Path(agents_path).parent
            bridge = (
                "CLAUDE.md"
                if parent == Path(".")
                else (parent / "CLAUDE.md").as_posix()
            )
            append(f"{current_repo}:{bridge}", "repo-cascade-bridge")
    for agents_path in active_paths:
        append(f"{current_repo}:{agents_path}", "repo-cascade")

    payload_material = "\n".join(
        f"{source['delivery_path']}:{source['content_hash']}" for source in sources
    )
    return {
        "role": selection.role,
        "harness": selection.harness,
        "current_repo": current_repo,
        "cwd": cwd,
        "bytes": sum(source["bytes"] for source in sources),
        "tokens": sum(source["tokens"] for source in sources),
        "payload_hash": hashlib.sha256(payload_material.encode("utf-8")).hexdigest(),
        "sources": sources,
    }


def _aggregate(repositories: list[RepositoryRecord], kind: str) -> dict[str, int]:
    selected = [repo for repo in repositories if repo.kind == kind]
    documents = [document for repo in selected for document in repo.documents]
    paragraphs = [
        paragraph for document in documents for paragraph in document.paragraphs
    ]
    return {
        "repositories": len(selected),
        "present_repositories": sum(repo.present for repo in selected),
        "missing_root_agents": sum(
            repo.root_agents == "missing" for repo in selected
        ),
        "documents": len(documents),
        "bytes": sum(document.bytes for document in documents),
        "tokens": sum(document.tokens for document in documents),
        "paragraphs": len(paragraphs),
        "duplicate_paragraphs": sum(
            paragraph.duplicate_of is not None for paragraph in paragraphs
        ),
    }


def build_report(
    substrate_manifest: Path,
    fleet_manifest: Path,
    projects_root: Path,
    *,
    aosh_repo: str = DEFAULT_AOSH_REPO,
) -> dict[str, Any]:
    repositories = discover_repositories(
        substrate_manifest,
        fleet_manifest,
        projects_root,
        aosh_repo=aosh_repo,
    )
    candidates = [
        {
            "paragraph": paragraph.id,
            "classification": paragraph.classification,
            "destination": paragraph.destination,
            "basis": paragraph.basis,
            "duplicate_of": paragraph.duplicate_of,
        }
        for repo in repositories
        if repo.kind == "product"
        for document in repo.documents
        for paragraph in document.paragraphs
        if paragraph.classification != "repo-specific-unconditional"
    ]
    return {
        "format": FORMAT,
        "tokenizer": TOKENIZER,
        "repository_sets": {
            kind: _aggregate(repositories, kind)
            for kind in ("substrate", "product", "aosh")
        },
        "aosh": {
            "repo": aosh_repo,
            "global_load": False,
        },
        "repositories": [repo.as_dict() for repo in repositories],
        "clipping_candidates": candidates,
    }


def render_json(report: dict[str, Any]) -> str:
    return json.dumps(report, indent=2, sort_keys=True) + "\n"


def render_markdown(report: dict[str, Any]) -> str:
    sets = report["repository_sets"]
    lines = [
        "# AGENTS context inventory",
        "",
        f"* **Format** - `{report['format']}`",
        f"* **Tokenizer** - {report['tokenizer']}",
        "* **AOSH** - tracked separately, never part of global load",
        "",
        "## Repository sets",
        "",
    ]
    for kind in ("substrate", "product", "aosh"):
        aggregate = sets[kind]
        lines.append(
            f"* **{kind}** - {aggregate['repositories']} repositories, "
            f"{aggregate['documents']} documents, {aggregate['bytes']} bytes, "
            f"{aggregate['tokens']} tokens, "
            f"{aggregate['duplicate_paragraphs']} duplicate paragraphs"
        )
    lines.extend(["", "## Repositories", ""])
    for repo in report["repositories"]:
        lines.append(
            f"* **{repo['full_name']}** - {repo['kind']} - "
            f"{repo['visibility']} - root AGENTS {repo['root_agents']} - "
            f"{len(repo['documents'])} context documents"
        )
    lines.extend(["", "## Product clipping candidates", ""])
    candidates = report["clipping_candidates"]
    if not candidates:
        lines.append("* None.")
    for candidate in candidates:
        detail = (
            f"duplicate of `{candidate['duplicate_of']}`"
            if candidate["duplicate_of"]
            else candidate["basis"]
        )
        lines.append(
            f"* `{candidate['paragraph']}` - {candidate['classification']} - "
            f"{candidate['destination']} - {detail}"
        )
    return "\n".join(lines) + "\n"


def _parse_args(argv: list[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fleet-manifest", type=Path, required=True)
    parser.add_argument(
        "--substrate-manifest",
        type=Path,
        default=DEFAULT_SUBSTRATE_MANIFEST,
    )
    parser.add_argument("--projects-root", type=Path, default=DEFAULT_PROJECTS_ROOT)
    parser.add_argument("--aosh-repo", default=DEFAULT_AOSH_REPO)
    parser.add_argument(
        "--format", choices=("json", "markdown"), default="markdown"
    )
    parser.add_argument("--output", type=Path)
    parser.add_argument(
        "--check",
        action="store_true",
        help="fail when a repo is absent, visibility is unknown, or root AGENTS is missing",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(argv)
    try:
        report = build_report(
            args.substrate_manifest,
            args.fleet_manifest,
            args.projects_root,
            aosh_repo=args.aosh_repo,
        )
    except InventoryError as exc:
        print(f"agents-context-inventory: {exc}", file=sys.stderr)
        return 2
    rendered = (
        render_json(report) if args.format == "json" else render_markdown(report)
    )
    if args.output is None:
        print(rendered, end="")
    else:
        args.output.write_text(rendered, encoding="utf-8")
        print(f"wrote {args.output}")
    if not args.check:
        return 0
    incomplete = [
        repo
        for repo in report["repositories"]
        if not repo["present"]
        or repo["visibility"] == "unknown"
        or repo["root_agents"] == "missing"
    ]
    if incomplete:
        for repo in incomplete:
            print(
                f"incomplete: {repo['full_name']} present={repo['present']} "
                f"visibility={repo['visibility']} root_agents={repo['root_agents']}",
                file=sys.stderr,
            )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
