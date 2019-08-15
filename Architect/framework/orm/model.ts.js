import { plural } from '../utils/string.ts';
export class Model {
    getTable() {
        return this.table
            ? this.table
            : plural(this.constructor.name);
    }
}
//# sourceMappingURL=model.js.map