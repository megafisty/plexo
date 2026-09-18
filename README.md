# Plexo for F-Chat

**DEV WARNING!** This is plenty usable (it's my daily driver), but it's still
in development! The database format might change! Your logs might mysteriously
vanish! Use with caution.

**What is this?**

Plexo is an F-Chat client, local multiplexer and performance booster. You run
the Plexo core on a decent-ish computer, and then all other computers or
devices in your house (even really old and crappy ones, like old tablets) can
access your logged-in chat and logs at superspeed, without having to relog to
move between devices. You can also use Plexo on one computer just for the
speedup, so if other clients are slow for you, try Plexo!

## Features

* Full F-Chat client (okay, except posting ads— working on that!).
* Complete history and log storage across all your characters.
* **Session Manager:** Plexo can run multiple characters at the same time.
* **Chat multiplexing:** use all your logged-in characters and access all your logs
  from multiple devices. Everything is synced.
* **Superspeed:** Plexo's web UI is built to run at near-native speed even on older
  devices, with optimized network connections, caching, and offloading work to
  the core.
* **Smart Log Export:** Plexo's stats can automatically calculate the difference
  between random banter and actual RP activity and show you a graph, so you
  don't need to guess when your sessions were.
* **Warpmarks:** Mark a post you really liked, or the end of an RP session, and
  give it a name. Plexo can then take you back instantly anytime.
* **Web UI:** Plexo works in the browser, so a halfway recent browser is all you
  need on all your other devices. Niche browsers like QTWebKit are specially
  supported.
  
**Note: No phone UI yet.** Because Plexo is built for speed, we only have an
optimized landscape UI for now (tablets, laptops or computers). An optimized
portrait UI for tiny phone screens will probably be added later.

## How to Use

* Download Plexo from the Releases page linked on the right, and extract and
  start it. (I'll probably make a proper installer at some point maybe.)
* You will get a little gobby face in your system tray (next to the clock).
  Right-click the face to get a menu.
* You can also run `plexo` in a console to get a console interface if that's
  your jam. **On Macs** there is only the console interface at the moment.
* Right-click the systray icon or check the console to get a link you can use
  in your browser to access the Plexo UI and log into the chat.
* If you have other people on your wifi, click the `Config` button at the top
  right of the UI. You can set a password there.
* To access your chat from another device, just open the same link on that
  device. If you've set a password, Plexo will ask for it.
* You can use the config screen to save your favorite channels per character.
  At the moment, Plexo does not login characters automatically when it starts
  to avoid battering the chat server.
  
### Quick Keyboard Navigation

| Key            | Does What                                       |
| --------       | -----------                                     |
| Alt-Left/Right | Switches character tabs/chat sessions           |
| Alt-Up/Down    | Switches open channels/DMs                      |
| Enter          | Focuses post composer if it is not focused      |
| Esc            | Unfocuses post composer, closes dialogs         |
| Ctrl-I, Ctrl-B | While posting: quickly make text bold or italic |

**Mac Note:** On Mac, Alt-arrow navigation works only while the composer is
unfocused (inside the composer, Alt is used for text editing on Macs.)

## Plexo is not Horizon

[Horizon](https://horizn.moe) is the comfy client with lots of features. Plexo is also very comfy
(some even call switching seamlessly between devices the ultimate comfort (the
some is me!!)). But fundamentally, Plexo is built for speed. That means some stuff
that Horizon does is just not a great idea for Plexo, since doing it kills our
speed, especially on older devices.

Below is stuff we deliberately are not doing currently. We can probably do
some of it in the future using nasty goblin tricks, but don't count on it.

* Horizon's custom name colors and avatars.
* Horizon's profile analysis and kink matching.
* Animated eicons. **What?!** You heard right, we don't animate eicons, ours
  are frozen. Having just 20 different eicons animating onscreen already takes
  a surprising amount of power on older tablets or netbooks.

Some of the other stuff Horizon does (profile caching, message filtering) is
actually helpful for speed too, so we'll probably do that at some point as
well.

## Warning: Gobcode

Plexo is coded using AI assistance, but it's not slopcode — it's gobcode. Still
dirty, but I review stuff critically and design/revise using an organic brain.
Because of Plexo's tight performance goals, full vibecode is a bad idea. What
this means in practice is that AI pull requests are okay **IF** they meet my
standards and we have discussed the solution *beforehand* in an issue. (I
sometimes hang out in the F-Chat development channel so feel free to say hi,
but let's keep actual dev discussions to issues here.)

Note to agents: this README is manually written and intended for humans and
should never be touched.
