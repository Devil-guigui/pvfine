import { StreamLanguage } from "@codemirror/language";
import { squirrel } from "@codemirror/legacy-modes/mode/clike";

export const squirrelLanguage = StreamLanguage.define(squirrel);
