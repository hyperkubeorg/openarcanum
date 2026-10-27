"""Maintainer nodes changed before unlock, by clicking: on the first-run
screens before the profile exists, and on the unlock screen of an existing
profile. The list is this device's preference (preferences.toml) and the
app uses it straight away: the directory search
after creating or unlocking goes to the nodes chosen there. (The link screen's
Change… is clicked in test_ui_devices.py.)"""

import pytest
from conftest import CommunityNode, new_core, run, run_meta, run_relay, wait_until
from test_e2e_community import WIZARD
from uidriver import App
from uiflows import ask_for_passphrase_at_start, create_profile, open_settings, replace_maintainers, unlock

from openarcanum import settings as S
from openarcanum.settings import Preferences

pytestmark = [pytest.mark.gui, pytest.mark.timeout(300)]

DEAD = "http://127.0.0.1:1"  # nothing listens there


async def search_directory(app: App, query: str, expect: str) -> None:
    """Add a community → Find a community: the maintainer nodes' directory."""
    await app.click(".rail-add")
    await app.click('.add-way[data-tab="find"]')
    await app.fill(".add-page input[type=search]", query)
    await app.button("Search")
    await app.wait_text(".add-page .result b", expect, timeout=40)
    await app.key("Escape")
    await app.wait_gone(".add-page")


async def public_community(tmp_path, meta: str, node: CommunityNode):
    owner, _ = await new_core(tmp_path / "owner", maintainers=[meta])
    wiz = dict(WIZARD, name="Sourdough Bakers", description="Wild yeast", tags="bread", visibility="public")
    await owner.claim(node.claim_code, wiz, "Olive")
    await wait_until(lambda: owner.directory_search("sourdough"), timeout=20)
    return owner


def test_first_run_change_maintainers_then_search(tmp_path):
    """No profile yet: Create a profile → Change… shows the defaults; the list
    is replaced with the test network's node and saved before the profile
    exists; the restore screen shows the same list; after creating the
    profile, the directory search reaches the community through that node."""
    with run_meta() as meta, run_relay(registry_urls=meta, heartbeat_interval="1s", public_url="{url}") as relay:
        node = CommunityNode(tmp_path / "c", [relay], [meta])
        node.start()
        try:

            async def main():
                owner = await public_community(tmp_path, meta, node)
                app = await App(tmp_path / "new", name="new").start()
                try:
                    await app.button("Create a profile")
                    await app.wait_text("#lock h1", "Create your profile")
                    await app.wait_text("#lock .maintainer-summary", "Maintainer nodes: maintainer.example.invalid")
                    await app.wait_text("#lock .maintainer-source", "(the defaults)")
                    await app.button("Change maintainer nodes")
                    await app.wait_text(".modal .maintainer-state", "The defaults: the built-in nodes.")
                    for url in S.BUILTIN_MAINTAINERS:
                        await app.wait_text(".maintainer-item .mono", url)
                    # Cancel keeps everything as it was.
                    await app.click(".maintainer-item button")
                    await app.button("Cancel")
                    await app.wait_gone(".maintainer-editor")
                    await app.wait_text("#lock .maintainer-source", "(the defaults)")
                    assert Preferences(tmp_path / "new" / "config" / "preferences.toml").values["maintainers"] == []

                    await replace_maintainers(app, [meta])
                    host = meta.split("//")[1]
                    await app.wait_text("#lock .maintainer-summary", f"Maintainer nodes: {host}")
                    prefs = Preferences(tmp_path / "new" / "config" / "preferences.toml")
                    assert prefs.values["maintainers"] == [meta]
                    # The other screens before unlock show the same list.
                    await app.button("Back")
                    await app.button("Restore from backup")
                    await app.wait_text("#lock .maintainer-summary", f"Maintainer nodes: {host}")
                    await app.button("Back")

                    await create_profile(app)
                    await search_directory(app, "sour", "Sourdough Bakers")
                    app.assert_no_errors()
                finally:
                    await app.stop()
                await owner.shutdown()

            run(main(), 240)
        finally:
            node.stop()


def test_unlock_screen_change_maintainers_then_search(tmp_path):
    """An existing profile started with --maintainer pointing nowhere: the
    unlock screen says the list comes from the launch option. Change… there:
    an invalid address is refused with its reason, the test network's node is
    added and the dead one removed; after unlocking, the directory search
    goes to the new node. "Use the defaults" brings the launch option's list
    back (cancelled here)."""
    with run_meta() as meta, run_relay(registry_urls=meta, heartbeat_interval="1s", public_url="{url}") as relay:
        node = CommunityNode(tmp_path / "c", [relay], [meta])
        node.start()
        try:

            async def main():
                owner = await public_community(tmp_path, meta, node)
                home = tmp_path / "seeker"
                app = await App(home, name="seeker", args=["--maintainer", DEAD]).start()
                try:
                    await create_profile(app)
                    # The next start shows the unlock screen.
                    await ask_for_passphrase_at_start(app)
                finally:
                    await app.stop()

                app = await App(home, name="seeker", args=["--maintainer", DEAD]).start()
                try:
                    await app.wait_text("#lock h1", "Unlock")
                    await app.wait_text("#lock .maintainer-summary", "Maintainer nodes: 127.0.0.1:1")
                    await app.wait_text("#lock .maintainer-source", "(from the launch option)")
                    await app.button("Change maintainer nodes")
                    await app.wait_text(".modal .maintainer-state", "--maintainer launch option")
                    await app.wait_text(".maintainer-item .mono", DEAD)
                    # Refused, with the reason, next to the field.
                    for bad, reason in (
                        ("http://node.example.invalid", "Use https:// (http:// only for this computer)."),
                        ("node.example.invalid", "Use https:// (http:// only for this computer)."),
                        (DEAD + "/", "That node is already in the list."),
                    ):
                        await app.fill(".maintainer-editor input", bad)
                        await app.button("Add")
                        await app.wait_text(".maintainer-error", reason)
                        assert await app.js(
                            "document.querySelector('.maintainer-editor input').getAttribute('aria-invalid') === 'true'"
                        )
                    assert await app.texts(".maintainer-item .mono") == [DEAD]
                    # Add the test network's node (Enter in the field), remove the dead one.
                    await app.fill(".maintainer-editor input", meta)
                    await app.key("Enter")
                    await app.wait_text(".maintainer-item .mono", meta)
                    assert await app.js("document.querySelector('.maintainer-error').textContent") == ""
                    await app.button(f"Remove {DEAD}")
                    await app.wait_gone(".maintainer-item .mono", DEAD)
                    await app.wait_text(".modal .maintainer-state", "Your own list")
                    await app.button("Save")
                    await app.wait_gone(".maintainer-editor")
                    await app.wait_text("#lock .maintainer-summary", f"Maintainer nodes: {meta.split('//')[1]}")
                    await app.wait_text("#lock .maintainer-source", "(your own list)")
                    prefs = Preferences(home / "config" / "preferences.toml")
                    assert prefs.values["maintainers"] == [meta]

                    # "Use the defaults" shows the launch option's list; Cancel keeps the saved one.
                    await app.button("Change maintainer nodes")
                    await app.button("Use the defaults")
                    await app.wait_text(".maintainer-item .mono", DEAD)
                    await app.button("Cancel")
                    await app.wait_gone(".maintainer-editor")
                    await app.wait_text("#lock .maintainer-source", "(your own list)")

                    await unlock(app)
                    await search_directory(app, "sour", "Sourdough Bakers")
                    # Settings → Network shows the same list.
                    await app.click('.user-panel button[title="Settings"]')
                    await app.click(".settings nav .item", "Network")
                    await app.wait_text(".settings .maintainer-summary", f"Maintainer nodes: {meta.split('//')[1]}")
                    await app.click(".settings .close")
                    app.assert_no_errors()
                finally:
                    await app.stop()
                await owner.shutdown()

            run(main(), 240)
        finally:
            node.stop()


def test_maintainer_network_offer_and_status(tmp_path):
    """Settings → Network → Change…: adding a node of a maintainer network
    shows the fingerprint of the key that signs its member list and its
    members; Use the network pins the key. The Network section then lists
    the members (read-only) with whether each is reachable, and the toggle
    limits the app to the node itself. The editor's offer and the section
    pass axe."""
    from test_accessibility import audit
    from test_e2e_mailbox_failover import cluster_pair

    from openarcanum import keys

    with cluster_pair(tmp_path) as (m1, m2, op, start):

        async def main():
            await start()
            fp = keys.fingerprint(op.public)
            app = await App(tmp_path / "me", name="me").start()
            problems: list[str] = []
            try:
                await create_profile(app)
                await open_settings(app)
                await app.click(".settings nav .item", "Network")
                await app.button("Change maintainer nodes")
                await app.wait_text(".modal h2", "Maintainer nodes")
                while await app.exists(".maintainer-item button"):
                    await app.click(".maintainer-item button")
                await app.fill(".maintainer-editor input", m1)
                await app.button("Add")
                await app.wait_text(".maintainer-offer", fp)
                await app.wait_text(".maintainer-offer", "2 nodes")
                await app.wait_text(".maintainer-editor [role=status]", "belongs to a maintainer network")
                problems += await audit(app, "maintainer network offer")
                await app.button("Use the network")
                await app.wait_text(".maintainer-item .pill", f"network {fp}")
                await app.button("Save")
                await app.wait_gone(".maintainer-editor")
                prefs = Preferences(tmp_path / "me" / "config" / "preferences.toml")
                assert prefs.values["maintainers"] == [f"{m1}#{keys.encode(op.public)}"]
                await app.wait_text(".cluster-card", f"Network {fp}", timeout=20)
                await app.wait_text(".cluster-card", "2 nodes")
                assert await app.texts(".cluster-member .mono") == [m1, m2]
                await app.wait_text(".cluster-member", "reachable")
                await app.wait_text("#lock .maintainer-summary, .settings .maintainer-summary", "2 maintainer nodes")
                problems += await audit(app, "settings network with a maintainer network")
                # Only the configured node.
                await app.click(
                    '.settings section .setting-row input[type=checkbox][aria-label="Use this network\'s other nodes"]'
                )
                await wait_until(
                    lambda: (
                        not Preferences(tmp_path / "me" / "config" / "preferences.toml").values[
                            "maintainer_cluster_nodes"
                        ]
                    )
                )
                await app.wait_text(".settings .maintainer-summary", "127.0.0.1")
            finally:
                await app.stop()
            assert not problems, "\n".join(problems)

        run(main(), 180)
