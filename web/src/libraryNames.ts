/**
 * What the libraries are called, which follows whether people sign in.
 *
 * - Sign-in off (the Mac and Linux apps, a Studio one person uses): the
 *   main library is the person's own folder, read and written as them, and
 *   is called My library unless they named it. A folder of theirs on a
 *   system is another library, named by its folder and "(on <system>)".
 * - Sign-in on: the main library is the one people share (Shared library,
 *   or the deployment's name), read only for everyone but its
 *   administrators, and My library is each person's folder on a system.
 *
 * The server names the main library the same way (settings.MainLabel), and
 * names a folder of the person's own by its folder where nobody signs in
 * (libraries.UserDef.ShownLabel).
 */
export interface NamingConfig { signIn?: boolean; kbLabel: string }

export interface LibraryNames {
  /** The main library, as the switcher shows it. */
  main: string
  /** A person's own folder on a system, as a noun: "My library", or "a
   *  folder of yours on one of your systems". */
  own: string
  /** Where agent instructions and a person's own files go, as a phrase:
   *  "the main library or My library", or "<main> or a folder of yours on
   *  one of your systems". */
  writable: string
}

export function libraryNames(cfg: NamingConfig): LibraryNames {
  if (cfg.signIn) return { main: cfg.kbLabel, own: 'My library', writable: 'the main library or My library' }
  return { main: cfg.kbLabel, own: 'a folder of yours on one of your systems', writable: `${cfg.kbLabel} or a folder of yours on one of your systems` }
}
