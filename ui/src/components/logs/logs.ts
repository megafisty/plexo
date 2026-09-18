// logs.ts — the chatlog modal. A small tabbed shell: "Export Chatlogs" (the
// two-sided browser/exporter) and "Cleanup". Closing unmounts the active tab,
// so a reopen starts clean. The tab bodies live in export.ts and cleanup.ts;
// their shared picker, labels, activity strip, and helpers are siblings in this
// folder.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useView } from "../../context.js";
import { closeModal } from "../../store/state.js";
import { Dialog, DialogTabs } from "../primitives/dialog.js";
import { Cleanup } from "./cleanup.js";
import { ExportChatlogs } from "./export.js";

type LogsTab = "export" | "cleanup";

interface LogsDialogState {
	tab: LogsTab;
}

export const LogsDialog: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as LogsDialogState;
		state.tab = "export";
	},
	view: (vnode) => {
		const view = useView();
		const state = vnode.state as LogsDialogState;
		const close = (): void => {
			closeModal(view);
		};
		return m(
			Dialog,
			{ title: "Chatlogs", onClose: close, class: "logs-dialog" },
			m(DialogTabs, {
				active: state.tab,
				onSelect: (id) => {
					state.tab = id as LogsTab;
				},
				tabs: [
					{
						id: "export",
						label: "Export Chatlogs",
						render: () => m(ExportChatlogs),
					},
					{ id: "cleanup", label: "Cleanup", render: () => m(Cleanup) },
				],
			}),
		);
	},
};