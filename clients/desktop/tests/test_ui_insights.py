"""Community Admin → Insights (moderation.js adminInsights): the day's
activity as counts, for admins; a member without the permission has no
such section."""

import pytest
from conftest import network, run
from uidriver import App
from uiflows import claim, community_section, create_profile, join, open_community_menu, send_message

pytestmark = pytest.mark.gui


def test_insights_show_the_days_activity(tmp_path):
    with network(tmp_path) as (_meta, _relay, community):
        node = community()

        async def main():
            olive = await App(tmp_path / "olive", name="olive").start()
            alice = await App(tmp_path / "alice", name="alice").start()
            try:
                for app in (olive, alice):
                    await create_profile(app)
                invite = await claim(olive, node.claim_code, "Busy Town", "Olive")
                await join(alice, invite, "Alice")
                for text in ("one", "two"):
                    await send_message(alice, text)
                await send_message(olive, "three")
                await olive.wait_text("#messages .mtext", "two")

                await community_section(olive, "Insights")
                await olive.wait_js("document.querySelector('.insight-tiles')", "the insight tiles")
                tiles = await olive.js(
                    "Object.fromEntries([...document.querySelectorAll('.insight-tile')]"
                    ".map((e) => [e.querySelector('.muted').textContent,"
                    " Number(e.querySelector('.insight-value').textContent)]))"
                )
                assert tiles["Members now"] == 2 and tiles["Joined"] == 1, tiles
                assert tiles["Messages"] >= 3, tiles
                assert await olive.exists(".insight-channels td", "#general")
                # 30 days by default; 7 on request.
                assert await olive.js("document.querySelectorAll('.insight-chart')[0].children.length") == 30
                await olive.select(".settings section select", "Last 7 days")
                await olive.wait_js("document.querySelectorAll('.insight-chart')[0].children.length === 7", "7 days")

                # Alice (no Manage community, no View audit log) has no
                # Community Admin, so no Insights.
                await open_community_menu(alice)
                assert not await alice.exists(".menu .mi", "Community Admin")
                await alice.key("Escape")
                for app in (olive, alice):
                    app.assert_no_errors()
            finally:
                await olive.stop()
                await alice.stop()

        run(main(), 240)
