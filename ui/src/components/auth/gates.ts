// gates.ts — the onboarding gates: core password then F-Chat credentials.
// Absorbs CoreLoginGate.ts and CredentialsGate.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useView, useStore } from "../../context.js";
import { request } from "../../render.js";
import { Button, Card, Checkbox, FormError, TextField } from "../primitives/form.js";


// ==========================================================================
// CoreLoginGate.ts
// ==========================================================================
// CoreAuthGate: sign in to the core with the shared Plexo password. Skipped
// entirely when the core does not require one.

export function CoreLoginGate(): Mithril.Component {
	const view = useView();
	const actions = useActions();
	let password = "";
	let busy = false;

	const submit = (): void => {
		if (busy || password === "") {
			return;
		}
		busy = true;
		void actions.authenticate(password).then((ok) => {
			busy = false;
			view.coreAuthError = ok ? null : "Incorrect password.";
			request();
		});
	};

	return {
		view: () =>
			m(
				Card,
				{
					title: "Unlock Plexo for this device",
					subtitle: "Enter the shared password to continue.",
				},
				[
					m(TextField, {
						label: "Password",
						type: "password",
						value: password,
						autocomplete: "current-password",
						disabled: busy,
						oninput: (value) => {
							password = value;
						},
						onsubmit: submit,
					}),
					m(FormError, { message: view.coreAuthError }),
					m(Button, {
						label: "Sign in",
						busy,
						disabled: password === "",
						onclick: submit,
					}),
				],
			),
	};
}

// ==========================================================================
// CredentialsGate.ts
// ==========================================================================
// CredentialsGate: supply the F-Chat account and password. The credentials
// travel browser -> core once over the WebSocket; the core validates them by
// minting a ticket and never returns the password or the ticket. When
// "Remember on this server" is checked a validated pair is stored unencrypted
// in the core's database so the gate does not reappear after a restart.
export function CredentialsGate(): Mithril.Component {
	const store = useStore();
	const view = useView();
	const actions = useActions();
	let account = "";
	let password = "";
	let remember = false;
	let busy = false;

	const submit = (): void => {
		if (busy || account === "" || password === "") {
			return;
		}
		busy = true;
		view.credentialsError = null;
		void actions.setCredentials(account, password, remember).then((err) => {
			busy = false;
			if (err !== null) {
				view.credentialsError = err;
			}
			request();
		});
	};

	return {
		view: () => {
			const checking = store.account.status === "checking" || busy;
			return m(
				Card,
				{
					title: "F-List account",
				},
				[
					m(TextField, {
						label: "Account",
						value: account,
						autocomplete: "username",
						disabled: checking,
						oninput: (value) => {
							account = value;
						},
						onsubmit: submit,
					}),
					m(TextField, {
						label: "Password",
						type: "password",
						value: password,
						autocomplete: "current-password",
						disabled: checking,
						oninput: (value) => {
							password = value;
						},
						onsubmit: submit,
					}),
					m(Checkbox, {
						label: "Remember and don't ask again",
						checked: remember,
						disabled: checking,
						onchange: (value: boolean) => {
							remember = value;
						},
					}),
					m(
						"p.field-note",
						"Remembered credentials are stored unencrypted in the core's database. Anyone with direct access to that database can read them.",
					),
					m(FormError, { message: view.credentialsError }),
					m(Button, {
						label: "Continue",
						busy: checking,
						disabled: account === "" || password === "",
						onclick: submit,
					}),
				],
			);
		},
	};
}
