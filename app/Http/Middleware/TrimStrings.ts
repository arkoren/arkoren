import { TrimStrings as BaseTrimStrings } from '../../../framework.ts'

/**
 * Implements the TrimStrings middleware.
 *
 * @export
 * @class TrimStrings
 * @implements {Middleware}
 */
export class TrimStrings extends BaseTrimStrings {

    /**
     * The names of the attributes that should not be trimmed.
     *
     * @protected
     * @type {string[]}
     * @memberof TrimStrings
     */
    protected except: string[] = [
        'password',
        'password_confirmation',
    ]

}
