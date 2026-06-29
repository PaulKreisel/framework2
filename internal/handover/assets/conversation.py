"""
Usage:
    python3 conversation.py <bead-id>        # last agent-mode conversation only
    python3 conversation.py <bead-id> --all  # all agent-mode conversations
"""
import json, subprocess, sys, textwrap

def format_conversation(agent, index=None):
    header_parts = [f"mode={agent.get('mode')} status={agent.get('status')}"]
    if agent.get('startedAt'):
        header_parts.append(f"started={agent['startedAt']}")
    label = f"--- Conversation {index} " if index is not None else "--- Conversation "
    label += f"({', '.join(header_parts)}) ---"

    lines = [label, ""]
    for entry in (agent.get('conversation') or []):
        t = entry.get('type')
        if t == 'user':
            prompt = entry.get('prompt', '').strip()
            if prompt == 'Ignore the kill, adding was successful. Continue':
                continue
            lines.append(f"[user] {prompt}")
        elif t in ('assistant_comment', 'assistant_reply'):
            text = entry.get('text', '').strip()
            if text == 'No response requested.':
                continue
            wrapped = textwrap.indent(textwrap.fill(text, width=100), '  ')
            lines.append(f"[assistant]\n{wrapped}")
        lines.append("")
    return "\n".join(lines)

def main():
    args = sys.argv[1:]
    if not args or args[0] in ('-h', '--help'):
        print(__doc__)
        sys.exit(0)

    bead_id = args[0]
    show_all = '--all' in args

    data = json.loads(subprocess.check_output(['bd', 'show', bead_id, '--json']))
    d = data[0] if isinstance(data, list) else data

    agents = [a for a in (d.get('metadata') or {}).get('agents', []) if a.get('mode') == 'agent']

    if not agents:
        print(f"No agent-mode conversations found on {bead_id}.")
        sys.exit(0)

    if show_all:
        selected = list(enumerate(agents))
    else:
        selected = [(len(agents) - 1, agents[-1])]

    for i, agent in selected:
        idx = i if show_all else None
        print(format_conversation(agent, index=idx))

if __name__ == '__main__':
    main()
