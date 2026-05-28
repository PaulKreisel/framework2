import json, subprocess, re, sys

EPIC = sys.argv[1] if len(sys.argv) > 1 else None

# Find epic id: the root bead (no dot, no other prefix child)
listing = subprocess.check_output(['bd', 'list', '--all'], text=True)

if not EPIC:
    # First non-child line that looks like an id
    m = re.search(r'○|◐|●|✓|❄', listing)
    if m:
        EPIC = listing[m.end():].split()[1]

# Children
children = re.findall(r'[├└]──\s+\S+\s+(\S+)', listing)

# Child metadata
child_meta = {}  # id -> {title, idx}
for cid in children:
    data = json.loads(subprocess.check_output(['bd', 'show', cid, '--json']))
    d = data[0] if isinstance(data, list) else data
    ho = (d.get('metadata') or {}).get('handover') or {}
    child_meta[cid] = {
        'title': d.get('title', ''),
        'idx': ho.get('contributionIndex'),
    }

# Epic PRs
epic_data = json.loads(subprocess.check_output(['bd', 'show', EPIC, '--json']))
e = epic_data[0] if isinstance(epic_data, list) else epic_data
prs = (e.get('metadata') or {}).get('prs', [])
repos = [pr['repo'] for pr in prs]

# Commits per (subtask_index, repo)
table = {}  # idx -> {repo -> [entry, ...]}
for pr in prs:
    repo, num = pr['repo'], pr['number']
    data = json.loads(subprocess.check_output(
        ['gh', 'pr', 'view', str(num), '--repo', repo, '--json', 'commits']))
    for c in data['commits']:
        full = c['messageHeadline'] + '\n' + (c.get('messageBody') or '')
        m = re.search(r'Subtask\s+(\S+)', full, re.IGNORECASE)
        if not m:
            continue
        idx = int(m.group(1))
        table.setdefault(idx, {}).setdefault(repo, []).append(
            f"{c['oid'][:8]} {c['messageHeadline']}")

# Print table
repo_shorts = [r.split('/')[-1] for r in repos]
col_headers = ' | '.join(f'{r}#{pr["number"]}' for r, pr in zip(repo_shorts, prs))
print(f'| Subtask | Child | Title | {col_headers} |')
print(f'|---------|-------|-------|' + '|'.join(['---'] * len(repos)) + '|')

# Rows with a contributionIndex, sorted
indexed = sorted(
    [(cm['idx'], cid, cm) for cid, cm in child_meta.items() if cm['idx']],
    key=lambda x: x[0])
for idx, cid, cm in indexed:
    cells = []
    for repo in repos:
        entries = table.get(idx, {}).get(repo, [])
        cells.append('<br>'.join(entries) if entries else '—')
    print(f"| {idx} | {cid} | {cm['title']} | " + ' | '.join(cells) + ' |')

# Rows without a contributionIndex
for cid, cm in child_meta.items():
    if cm['idx'] is None:
        print(f"| — | {cid} | {cm['title']} | " + ' | '.join(['—'] * len(repos)) + ' |')
